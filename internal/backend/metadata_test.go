package backend

import (
	"strings"
	"testing"
)

func TestDiffReconstructIncludesDiscard(t *testing.T) {
	base := []Mapping{{0, 10, 4, 1}, {8, 30, 2, 1}}
	target := []Mapping{{0, 10, 1, 1}, {1, 50, 1, 2}, {3, 13, 1, 1}, {9, 31, 1, 1}, {12, 90, 1, 2}}
	got := Diff(base, target, 512, 8192, 0, true)
	want := [][2]int64{{512, 1024}, {4096, 512}, {6144, 512}}
	if len(got) != len(want) {
		t.Fatalf("ranges: %v", got)
	}
	for i, r := range got {
		if r.Offset != want[i][0] || r.Length != want[i][1] {
			t.Fatalf("range %d: %v", i, r)
		}
	}
	continued := Diff(base, target, 512, 8192, 800, true)
	if continued[0].Offset != 800 || continued[0].Length != 736 {
		t.Fatal(continued)
	}
}
func TestParseThinDump(t *testing.T) {
	xml := `<superblock data_block_size="128"><device dev_id="1"><range_mapping origin_begin="0" data_begin="42" length="2" time="3"/></device><device dev_id="2"><single_mapping origin_block="5" data_block="90" time="4"/></device></superblock>`
	block, m, e := ParseThinDump(strings.NewReader(xml), map[int64]bool{2: true})
	if e != nil || block != 65536 || len(m) != 1 || m[2][0].Start != 5 {
		t.Fatalf("%d %v %v", block, m, e)
	}
	if _, _, e = ParseThinDump(strings.NewReader(xml), map[int64]bool{3: true}); e == nil {
		t.Fatal("missing device accepted")
	}
}
func TestDiffSameSnapshot(t *testing.T) {
	m := []Mapping{{0, 2, 20, 1}}
	if len(Diff(m, m, 65536, 20*65536, 0, true)) != 0 {
		t.Fatal("unchanged snapshot reported changes")
	}
}
