package review

import (
	"math/rand"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestOrdinalComparisonUsesNoAllocations(t *testing.T) {
	if allocations := testing.AllocsPerRun(20, func() { ordinalCompare("src/Ж😀/LongClass.java", "src/Ж😁/LongClass.java") }); allocations != 0 {
		t.Fatalf("ordinal comparison allocated %.0f objects", allocations)
	}
}
func TestOrdinalComparisonMatchesUTF16Oracle(t *testing.T) {
	original := func(a, b string) int { return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b))) }
	values := []string{"", "ascii", "Ж", "😀", "😁", "\ue000", "\ufffd", string([]byte{0xff}), string([]byte{0xfe}), strings.Repeat("A", 128)}
	random := rand.New(rand.NewSource(397))
	for i := 0; i < 512; i++ {
		value := []rune{}
		for j := 0; j < i%31; j++ {
			value = append(value, rune(random.Intn(0x110000)))
		}
		values = append(values, string(value))
	}
	for i, a := range values {
		for _, b := range values[max(0, i-16):min(len(values), i+16)] {
			if got, want := ordinalCompare(a, b), original(a, b); got != want {
				t.Fatalf("UTF16 ordinal differs: %d/%d", got, want)
			}
		}
	}
}
func TestIntegerWireWeightMatchesJSON(t *testing.T) {
	for _, value := range []int{0, 1, 9, 10, 99, 100, 999, 1000, -1, -9, -10, -100, int(^uint(0) >> 1), -int(^uint(0)>>1) - 1} {
		if got, want := integerWeight(value), jsonWeight(value); got != want {
			t.Fatalf("integer weight %d differs: %d/%d", value, got, want)
		}
	}
}
