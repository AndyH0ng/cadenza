package metadata

import "testing"

// Cases (some trimmed) from a crawl of 5,192 Apple Music Classical tracks whose
// "movement" repeats the track title.
func TestPieceNumber(t *testing.T) {
	tests := []struct {
		title, work, catalog, want string
	}{
		{"Liebestraum No. 3 in A-Flat Major, S. 541 No. 3", "사랑의 꿈", "S. 541", "3"},
		{"Canción y Danza No. 6", "노래와 춤", "", "6"},
		{"Gnossienne No. 1 (Arr. for Double Bass & Piano by Thomas Gustavsson)", "그노시엔느", "", "1"},
		{"Prélude in E Minor, Op. 28 No. 4", "24개의 전주곡", "C. 166-189, Op. 28", "4"}, // "24" is not "4"
		{"4 Piano Pieces, Op. 119. No. 1 in B Minor. Intermezzo. Adagio", "4개의 소곡", "Op. 119", "1"},
		{"Brahms 6 Piano Pieces, Op. 118 No. 2, Intermezzo in A Major", "6개의 소곡", "Op. 118", "2"},

		// Numbers of an arrangement, in parentheses.
		{"Auf dem Wasser zu singen, Op. 72, D. 774 (Transcr. for Piano by Franz Liszt, S. 558 No. 2)",
			"물 위에서 노래한다 내림가장조", "D 774, Op. 72", ""},
		{"Adagio in E-Flat Major (Arr. Ólafsson from String Quintet No. 3 in G Minor, K. 516)", "현악 5중주 4번 사단조", "", ""},
		// Number after a catalog number that is not the work's.
		{"Litanei, D. 343, S. 562 No. 1", "모든 영혼을 기리는 날의 기도", "D 343", ""},
		// The work name already has it.
		{"Nocturne No. 1 in B-Flat Minor, Op. 9 No. 1", "야상곡 1번 내림나단조", "B. 54/1, Op. 9/1", ""},
		{"Invention No. 15 in B Minor, BWV 786", "인벤션 15번 나단조 · ‘2성 인벤션’", "BWV 786", ""},
		// No piece number.
		{"The Noonday Witch, Op. 108, B. 196", "폴레드니체 · ‘정오의 마녀’", "B 196, Op. 108", ""},
		{"Nocturne for Orchestra", "야상곡", "", ""},
		{"Die Kunst Der Füge BWV 1080 Contrapunctus No. 13B", "푸가의 기법", "BWV 1080", ""},
	}
	for _, tt := range tests {
		if got := PieceNumber(tt.title, tt.work, tt.catalog); got != tt.want {
			t.Errorf("PieceNumber(%q, %q, %q) = %q, want %q", tt.title, tt.work, tt.catalog, got, tt.want)
		}
	}
}
