package metadata

import (
	"regexp"
	"strconv"
	"strings"
)

// "[X. ][Genre ]No. 10 in A-Flat Major" and the rest of the movement title.
var numberedKeyRE = regexp.MustCompile(`(?i)^(?:([IVXLC]+)\.\s+)?(?:(\pL[\pL'’ -]*?)\s+)?No\.\s*(\d+)\s+in\s+([A-G])(?:\s*-\s*(flat|sharp)|\s+(flat|sharp))?\s+(major|minor)\b(.*)$`)

// Trailing parenthesized notes: "(Live)", "(Arr. for …)", "(Edgardo)".
var trailingNoteRE = regexp.MustCompile(`(?:\s*\([^()]*\))+\s*$`)

// Korean pitch names, as Apple writes them in Korean work titles.
var koreanNotes = map[string]string{"C": "다", "D": "라", "E": "마", "F": "바", "G": "사", "A": "가", "B": "나"}

// Genre words seen before "No. N" in Classical movements, with the Korean
// terms Apple uses in work titles. Keys are lower case.
var koreanGenres = map[string]string{
	"prelude": "전주곡", "prélude": "전주곡", "präludium": "전주곡",
	"fugue": "푸가", "fuga": "푸가",
	"étude": "연습곡", "etude": "연습곡",
	"waltz": "왈츠", "valse": "왈츠", "walzer": "왈츠",
	"ländler": "렌틀러",
}

// localMovement trims the movement, drops trailing parenthesized notes
// (performance, arrangement and role notes; brackets such as "I. [Allegro]"
// and inner parentheses are kept) and, for Korean, translates the
// unambiguous "[Genre ]No. N in <key>…" form. Other forms mix languages
// ("No. 5, Intermezzo in E Minor") or list several ("C minor / ut mineur")
// and are kept as Apple sends them, as are unknown genre words.
//
// Parts the work name already has are dropped: in "12개의 연습곡",
// "Étude No. 8 in D-Flat Major" becomes "8번 내림라장조"; in
// "전주곡과 푸가 1번 다장조", "Fugue No. 1 in C Major" becomes "푸가".
func localMovement(language, work, movement string) string {
	movement = strings.TrimSpace(trailingNoteRE.ReplaceAllString(movement, ""))
	if !strings.HasPrefix(strings.ToLower(language), "ko") {
		return movement
	}
	m := numberedKeyRE.FindStringSubmatch(movement)
	if m == nil {
		return movement
	}
	roman, genreWord, number, note, rest := m[1], m[2], m[3], m[4], m[8]
	// "X. No. 10": the roman numeral only repeats the number.
	if roman != "" && strconv.Itoa(romanValue(roman)) != number {
		return movement
	}
	var genre string
	if genreWord != "" {
		var ok bool
		if genre, ok = koreanGenres[strings.ToLower(genreWord)]; !ok {
			return movement
		}
	}
	accidental := map[string]string{"flat": "내림", "sharp": "올림"}[strings.ToLower(m[5]+m[6])]
	mode := "장조"
	if strings.EqualFold(m[7], "minor") {
		mode = "단조"
	}
	numberAndKey := number + "번 " + accidental + koreanNotes[strings.ToUpper(note)] + mode
	if strings.Contains(work, numberAndKey) {
		return genre // "" without a genre: the movement only repeats the work
	}
	if genre == "" || onlyGenreIn(work) == genre {
		return numberAndKey + rest
	}
	return genre + " " + numberAndKey + rest
}

// RepeatsTitle reports whether movement, as the Classical API sends it, only
// repeats the track title, which Apple does for works without movements.
// Trailing notes are ignored: Now Playing may show "… (Bonus Track)" where
// the movement has none.
func RepeatsTitle(movement, title string) bool {
	strip := func(s string) string { return strings.TrimSpace(trailingNoteRE.ReplaceAllString(s, "")) }
	return strip(movement) == strip(title)
}

// romanValue converts a roman numeral; invalid input gives a meaningless value.
func romanValue(s string) int {
	values := map[byte]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100}
	s = strings.ToUpper(s)
	total := 0
	for i := range len(s) {
		v := values[s[i]]
		if i+1 < len(s) && values[s[i+1]] > v {
			v = -v
		}
		total += v
	}
	return total
}

// onlyGenreIn returns the Korean genre term in work if there is exactly one:
// "12개의 연습곡" names its pieces' genre, "전주곡과 푸가" does not.
func onlyGenreIn(work string) string {
	var found string
	for _, g := range koreanGenres {
		if g != found && strings.Contains(work, g) {
			if found != "" {
				return ""
			}
			found = g
		}
	}
	return found
}
