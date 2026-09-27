package backend

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	pb "github.com/michaelcourcy/lvmo-csi/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Stream avoids buffering the entire pool's XML dump in memory.
func (Exec) Stream(ctx context.Context, name string, args []string, consume func(io.Reader) error) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	parseErr := consume(pipe)
	if parseErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if parseErr != nil {
		return parseErr
	}
	if waitErr != nil {
		return fmt.Errorf("%s: %w: %s", name, waitErr, stderr.String())
	}
	return nil
}

type Mapping struct{ Start, Physical, Length, Time int64 }

// ParseThinDump consumes metadata, never volume data. Only selected device maps
// are retained. thin_dump runs against a kernel-reserved metadata snapshot.
func ParseThinDump(r io.Reader, ids map[int64]bool) (int64, map[int64][]Mapping, error) {
	dec := xml.NewDecoder(r)
	maps := map[int64][]Mapping{}
	var block int64
	var current int64 = -1
	for {
		tok, e := dec.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return 0, nil, e
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		a := map[string]int64{}
		for _, v := range el.Attr {
			n, _ := strconv.ParseInt(v.Value, 10, 64)
			a[v.Name.Local] = n
		}
		switch el.Name.Local {
		case "superblock":
			block = a["data_block_size"] * 512
		case "device":
			current = a["dev_id"]
			if ids[current] {
				maps[current] = []Mapping{}
			}
		case "range_mapping":
			if ids[current] {
				maps[current] = append(maps[current], Mapping{a["origin_begin"], a["data_begin"], a["length"], a["time"]})
			}
		case "single_mapping":
			if ids[current] {
				maps[current] = append(maps[current], Mapping{a["origin_block"], a["data_block"], 1, a["time"]})
			}
		}
	}
	if block <= 0 {
		return 0, nil, fmt.Errorf("missing thin metadata block size")
	}
	for id := range ids {
		if _, ok := maps[id]; !ok {
			return 0, nil, fmt.Errorf("thin device %d missing from metadata", id)
		}
	}
	return block, maps, nil
}

// Diff includes removed mappings: reading those ranges from the target thin LV
// yields zeros, preventing stale bytes in an incrementally reconstructed image.
func Diff(base, target []Mapping, block, capacity, start int64, delta bool) []*pb.Range {
	bounds := []int64{}
	for _, list := range [][]Mapping{base, target} {
		for _, m := range list {
			bounds = append(bounds, m.Start, m.Start+m.Length)
		}
	}
	sort.Slice(bounds, func(i, j int) bool { return bounds[i] < bounds[j] })
	var out []*pb.Range
	bi, ti := 0, 0
	for i := 0; i+1 < len(bounds); i++ {
		lo, hi := bounds[i], bounds[i+1]
		if hi <= lo {
			continue
		}
		for bi < len(base) && base[bi].Start+base[bi].Length <= lo {
			bi++
		}
		for ti < len(target) && target[ti].Start+target[ti].Length <= lo {
			ti++
		}
		bp, tp := int64(-1), int64(-1)
		bt, tt := int64(-1), int64(-1)
		if bi < len(base) && base[bi].Start <= lo {
			bp = base[bi].Physical + lo - base[bi].Start
			bt = base[bi].Time
		}
		if ti < len(target) && target[ti].Start <= lo {
			tp = target[ti].Physical + lo - target[ti].Start
			tt = target[ti].Time
		}
		changed := tp >= 0
		if delta {
			changed = bp != tp || bt != tt
		}
		if !changed {
			continue
		}
		offset, end := lo*block, hi*block
		if offset < start {
			offset = start
		}
		if end > capacity {
			end = capacity
		}
		if end <= offset {
			continue
		}
		if len(out) > 0 && out[len(out)-1].Offset+out[len(out)-1].Length == offset {
			out[len(out)-1].Length += end - offset
		} else {
			out = append(out, &pb.Range{Offset: offset, Length: end - offset})
		}
	}
	return out
}
func (b *Backend) thinID(ctx context.Context, s *pb.Snapshot) (int64, error) {
	out, e := b.run.Run(ctx, "lvs", "--noheadings", "-o", "thin_id", device(s.Vg, s.Id))
	if e != nil {
		return 0, e
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}
func (b *Backend) metadata(ctx context.Context, r *pb.MetadataRequest) (int64, []*pb.Range, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.Snapshot == "" || r.StartingOffset < 0 || r.MaxResults < 0 {
		return 0, nil, status.Error(codes.InvalidArgument, "invalid metadata request")
	}
	s := b.state.Snapshots[r.Snapshot]
	if s == nil || !s.Ready || b.state.SnapshotDeleting[s.Id] {
		return 0, nil, status.Error(codes.NotFound, "snapshot not found")
	}
	if r.StartingOffset > s.Bytes {
		return 0, nil, status.Error(codes.OutOfRange, "offset exceeds snapshot")
	}
	tid, e := b.thinID(ctx, s)
	if e != nil {
		return 0, nil, internal(e)
	}
	ids := map[int64]bool{tid: true}
	bid := int64(-1)
	if r.BaseSnapshot != "" {
		base := b.state.Snapshots[r.BaseSnapshot]
		if base == nil || !base.Ready || b.state.SnapshotDeleting[base.Id] {
			return 0, nil, status.Error(codes.NotFound, "base snapshot not found")
		}
		ordered := base.CreatedUnix < s.CreatedUnix
		if b.state.SnapshotOrder[base.Id] > 0 && b.state.SnapshotOrder[s.Id] > 0 {
			ordered = b.state.SnapshotOrder[base.Id] < b.state.SnapshotOrder[s.Id]
		}
		if base.Id == s.Id || base.VolumeId != s.VolumeId || base.Vg != s.Vg || !ordered {
			return 0, nil, status.Error(codes.InvalidArgument, "snapshots must be ordered and belong to the same volume")
		}
		bid, e = b.thinID(ctx, base)
		if e != nil {
			return 0, nil, internal(e)
		}
		ids[bid] = true
	}
	escape := func(s string) string { return strings.ReplaceAll(s, "-", "--") }
	pool := escape(s.Vg) + "-" + escape(b.cfg.Pool) + "-tpool"
	meta := "/dev/mapper/" + escape(s.Vg) + "-" + escape(b.cfg.Pool) + "_tmeta"
	if e = b.cmd(ctx, "dmsetup", "message", pool, "0", "reserve_metadata_snap"); e != nil {
		return 0, nil, internal(e)
	}
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := b.cmd(release, "dmsetup", "message", pool, "0", "release_metadata_snap"); err != nil {
			log.Printf("release thin metadata snapshot: %v", err)
		}
	}()
	var block int64
	var maps map[int64][]Mapping
	parse := func(reader io.Reader) error { var err error; block, maps, err = ParseThinDump(reader, ids); return err }
	if runner, ok := b.run.(interface {
		Stream(context.Context, string, []string, func(io.Reader) error) error
	}); ok {
		e = runner.Stream(ctx, "thin_dump", []string{"--metadata-snap", meta}, parse)
	} else {
		var raw []byte
		raw, e = b.run.Run(ctx, "thin_dump", "--metadata-snap", meta)
		if e == nil {
			e = parse(bytes.NewReader(raw))
		}
	}
	if e != nil {
		return 0, nil, internal(e)
	}
	return s.Bytes, Diff(maps[bid], maps[tid], block, s.Bytes, r.StartingOffset, r.BaseSnapshot != ""), nil
}
func (b *Backend) Metadata(r *pb.MetadataRequest, stream grpc.ServerStreamingServer[pb.Ranges]) error {
	capacity, ranges, e := b.metadata(stream.Context(), r)
	if e != nil {
		return e
	}
	n := int(r.MaxResults)
	if n == 0 || n > 1024 {
		n = 1024
	}
	if len(ranges) == 0 {
		return stream.Send(&pb.Ranges{Capacity: capacity})
	}
	for len(ranges) > 0 {
		count := min(n, len(ranges))
		if e = stream.Send(&pb.Ranges{Capacity: capacity, Ranges: ranges[:count]}); e != nil {
			return e
		}
		ranges = ranges[count:]
	}
	return nil
}
