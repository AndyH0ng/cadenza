package metadata

import (
	"regexp"
	"slices"
	"strings"
)

var (
	parenthesizedRE = regexp.MustCompile(`\([^)]*\)|\[[^\]]*\]`)
	// "No. 3", optionally preceded by the catalog it belongs to ("Op. 28 No. 4").
	pieceNumberRE = regexp.MustCompile(`(?:\b([A-Z][A-Za-z]{0,3})\.?\s*(\d+[a-z]?)\s*,?\s*)?\bNo\.?\s*(\d+)\b`)
	numberRE      = regexp.MustCompile(`\d+`)
)

// PieceNumber returns the number of a piece within its work, taken from the
// track title, when the localized work name lacks it. For "Liebestraum No. 3
// in A-Flat Major, S. 541 No. 3" in the work "사랑의 꿈" (S. 541) it returns "3".
//
// It returns "" when unsure:
//   - text in parentheses is ignored; it describes arrangements
//     ("Transcr. … by Liszt, S. 558 No. 2" numbers the transcription set);
//   - a number after a catalog number counts only if that catalog number is
//     the work's own (catalog);
//   - the work name already containing the number ("야상곡 1번") yields "".
func PieceNumber(title, work, catalog string) string {
	title = parenthesizedRE.ReplaceAllString(title, "")
	for _, m := range pieceNumberRE.FindAllStringSubmatch(title, -1) {
		if m[1] != "" && !strings.Contains(normalizeCatalog(catalog), normalizeCatalog(m[1]+m[2])) {
			continue
		}
		if slices.Contains(numberRE.FindAllString(work, -1), m[3]) {
			return ""
		}
		return m[3]
	}
	return ""
}

// normalizeCatalog makes "D. 774", "D 774" and "d774" compare equal.
func normalizeCatalog(s string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", ".", "").Replace(s))
}
