# 개발

빌드 방법은 [README](README.md#소스에서-빌드)에 있습니다.

## 테스트

```sh
go vet ./... && go test -race ./...
```

테스트는 Discord, Apple API, adapter를 가짜로 대체하므로 네트워크나 Discord 없이 돌아갑니다.

커버리지는 아래처럼 확인합니다. README의 커버리지 배지는 `main`에 push할 때마다 CI가 `badges` 브랜치에 갱신합니다.

```sh
go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1
```

## 구조

| 패키지 | 역할 |
|---|---|
| [`internal/nowplaying`](internal/nowplaying) | adapter와 AppleScript로 재생 상태 읽기 |
| [`internal/metadata`](internal/metadata) | Apple Music Classical, Apple Music, iTunes 조회와 한국어 제목 규칙 |
| [`internal/presence`](internal/presence) | 곡을 Discord 활동으로 변환 (I/O 없음) |
| [`internal/discord`](internal/discord) | Discord IPC 클라이언트 |
| [`internal/app`](internal/app) | 위 패키지 연결, 전송 제한, 재시도 |

macOS 15.4부터는 권한 있는 바이너리만 MediaRemote를 쓸 수 있습니다. 그래서 adapter를 `/usr/bin/perl` 안에서 실행합니다.

## 골든 테스트

[`internal/app/testdata/classical_sample.json`](internal/app/testdata/classical_sample.json)에는 Apple Music Classical 곡 131개의 API 응답 필드가 있습니다. 각 제목 규칙의 대표 곡들입니다. `TestGoldenSample`은 이 곡들의 표시 제목을 [`classical_sample.golden`](internal/app/testdata/classical_sample.golden)과 비교합니다.

- 제목 규칙을 바꿨다면 `go test ./internal/app -run Golden`으로 달라진 곡을 확인합니다. 의도한 변경이면 `-update`로 갱신하고 diff를 검토합니다.
- 잘못 표시되는 곡을 찾았다면 그 곡의 API 필드를 `classical_sample.json`에 추가합니다. `-update`로 현재 결과를 기록한 뒤 규칙을 고치고 diff를 확인합니다.

더 큰 수집 데이터를 `testdata/classical_tracks.json`에 두면 `TestGoldenAll`이 `classical_titles.golden`과 비교합니다. 이 파일들은 저장소에 포함하지 않습니다.

## Classical API

```
GET https://classical.music.apple.com/api/classical/v10/query/tracksMetadata/<스토어프론트>?kind=song&ids=<곡 ID>&l=<언어>
```

- 인증이 필요 없습니다. 곡 ID는 Now Playing의 `uniqueIdentifier`입니다.
- `l`을 빼면 영어로 응답합니다. `platform=web`을 붙이면 400을 돌려줍니다.
- 악장명과 앨범 제목은 현지화되지 않습니다. 일부 악장명은 [`movement.go`](internal/metadata/movement.go)에서 직접 번역합니다.

## 릴리스

`v*` 태그를 push하면 [release 워크플로](.github/workflows/release.yml)가 유니버설 바이너리와 adapter를 묶어 GitHub Release에 올립니다.

```sh
git tag v0.1.0 && git push origin v0.1.0
```
