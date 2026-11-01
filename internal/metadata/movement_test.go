package metadata

import "testing"

// Cases from the Apple Music Classical crawl.
func TestLocalMovement(t *testing.T) {
	tests := []struct {
		language, work, movement, want string
	}{
		{"ko-KR", "12개의 연습곡", `No. 1 in A-Flat Major "Aeolian Harp"`, `1번 내림가장조 "Aeolian Harp"`},
		{"ko-KR", "24개의 카프리스", "No. 2 in B Minor", "2번 나단조"},
		{"ko-KR", "3개의 인터메초", "No. 2 in B-Flat Minor. Andante non troppo", "2번 내림나단조. Andante non troppo"},
		{"ko-KR", "평균율", "No. 8 in D -Sharp Minor", "8번 올림라단조"},
		{"ko-KR", "왈츠", "No. 3 in g minor", "3번 사단조"},
		// A roman numeral repeating the number is dropped; a different one is kept.
		{"ko-KR", "12개의 연습곡", "X. No. 10 in A-Flat Major. Vivace assai", "10번 내림가장조. Vivace assai"},
		{"ko-KR", "12개의 연습곡", "IV. No. 4 in C-Sharp Minor", "4번 올림다단조"},
		{"ko-KR", "12개의 연습곡", "II. No. 10 in A-Flat Major", "II. No. 10 in A-Flat Major"},
		// Genre words.
		{"ko-KR", "12개의 연습곡", `Étude No. 8 in D-Flat Major "Sixths"`, `8번 내림라장조 "Sixths"`},
		{"ko-KR", "클라브생 연주 기법", "Prélude No. 6 in B Minor", "전주곡 6번 나단조"},
		{"ko-KR", "무곡", `Waltz No. 1 in E Major "Atzenbrucker No. 1"`, `왈츠 1번 마장조 "Atzenbrucker No. 1"`},
		{"ko-KR", "무곡", "Ländler No. 1 in E-Flat Major", "렌틀러 1번 내림마장조"},
		{"ko-KR", "전주곡과 푸가 1번 다장조 ‘평균율 클라비어 곡집 2권’", "Prelude No. 1 in C Major, BWV 870", "전주곡"},
		{"ko-KR", "전주곡과 푸가 1번 다장조 ‘평균율 클라비어 곡집 2권’", "Fugue No. 1 in C Major, BWV 870", "푸가"},
		// Apple's data mismatches here; the genre must stay to tell prelude from fugue.
		{"ko-KR", "전주곡과 푸가 15번 사장조 ‘평균율 클라비어 곡집, 2권’", "Fugue No. 19 in A Major, BWV 888", "푸가 19번 가장조, BWV 888"},
		// The work name already has it.
		{"ko-KR", "인벤션 8번 바장조 ‘2성 인벤션’", "No. 8 in F Major, BWV 779 (Arr. for Violin and Viola)", ""},
		// Other forms are kept, trimmed.
		{"ko-KR", "피아노 협주곡 2번 바단조", " II. Larghetto", "II. Larghetto"},
		{"ko-KR", "평균율", "Sarabande No. 6 in B Minor", "Sarabande No. 6 in B Minor"}, // unknown genre
		{"ko-KR", "6개의 소곡", "No. 5, Intermezzo in E Minor", "No. 5, Intermezzo in E Minor"},
		{"ko-KR", "피아노 협주곡 22번", "II. Andante", "II. Andante"},
		// Trailing parenthesized notes are dropped.
		{"ko-KR", "피아노 협주곡 3번 라단조", "I. Allegro ma non tanto (Live from the 2022 Cliburn Competition)", "I. Allegro ma non tanto"},
		{"ko-KR", "미뉴에트", "III. Menuetto. Allegro  (Live)", "III. Menuetto. Allegro"},
		{"ko-KR", "람메르무어의 루치아", "Fra poco a me ricovero darà negletto avello (Edgardo)", "Fra poco a me ricovero darà negletto avello"},
		{"ko-KR", "모음곡", "VII. Gigue (Remastered) (Live)", "VII. Gigue"},
		{"ko-KR", "12개의 연습곡", `No. 3 in E Major "Tristesse" (Arr. Garrett & Haywood for Violin)`, `3번 마장조 "Tristesse"`},
		// Brackets and inner parentheses are kept.
		{"ko-KR", "모음곡", "I. [Allegro]", "I. [Allegro]"},
		{"ko-KR", "스타바트 마테르", "IV. Aria (alto): Quae moerebat et dolebat", "IV. Aria (alto): Quae moerebat et dolebat"},
		// Not Korean.
		{"en-US", "12 Études", " No. 1 in A-Flat Major", "No. 1 in A-Flat Major"},
	}
	for _, tt := range tests {
		if got := localMovement(tt.language, tt.work, tt.movement); got != tt.want {
			t.Errorf("localMovement(%q, %q, %q) = %q, want %q", tt.language, tt.work, tt.movement, got, tt.want)
		}
	}
}

func TestRepeatsTitle(t *testing.T) {
	tests := []struct {
		movement, title string
		want            bool
	}{
		{"Nocturne in C-Sharp Minor, Op. Posth.", "Nocturne in C-Sharp Minor, Op. Posth.", true},
		{"Nocturne in C-Sharp Minor, Op. Posth.", "Nocturne in C-Sharp Minor, Op. Posth. (Bonus Track)", true},
		{"Act 2 (Live)", "Lucio Silla: Act 2 (Live)", false},
		{"II. Andante", "Piano Concerto No. 22 in E-Flat Major, K. 482: II. Andante", false},
	}
	for _, tt := range tests {
		if got := RepeatsTitle(tt.movement, tt.title); got != tt.want {
			t.Errorf("RepeatsTitle(%q, %q) = %v, want %v", tt.movement, tt.title, got, tt.want)
		}
	}
}
