<div align="center">

# agentrec

**코딩 에이전트를 위한 플라이트 레코더 — 모든 실행은 터미널이 사라진 뒤에도 읽을 수 있는, 로컬에 저장된 귀속 정보가 붙은 증거 번들을 남긴다.**

[![CI](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml/badge.svg)](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[English](README.md) | 한국어 | [日本語](README.ja.md) | [简体中文](README.zh-CN.md)

</div>

## 문제

코딩 에이전트가 작업을 마치면 남는 것은 스크롤백뿐이다. 스크롤백은 위로 밀려
사라지고, 에이전트가 했다고 *말한* 것과 실제로 일어난 일을 뒤섞으며, 저장소가
여전히 빌드되는지에 대해서는 아무것도 말해주지 않는다.

agentrec은 비대화형 Claude Code 또는 Codex 실행 하나를 번들로 기록한다. 정규화된
액션 타임라인, 감독된 프로세스의 결과, 실행 구간 전후의 저장소 차이, 그리고
저장소 자신이 고정해 둔 검사의 결과가 담긴다. 각각은 서로 다른 관찰자에게서
나오며, 번들은 이들을 분리해 둔다.

## 네 가지 증거 계층

| 계층 | 관찰자 | 의미 | 기록되는 귀속 |
|---|---|---|---|
| **제공자 보고 액션** | 에이전트 | 에이전트가 했다고 말한 것 — 툴 호출, 셸 명령, 파일 읽기와 편집, MCP 호출, Codex 파일 변경. 정규화하고 요약하되, 결코 증명으로 취급하지 않는다. | `provider_reported` |
| **감독자 관찰 결과** | agentrec | 제공자 프로세스가 어떻게 끝났는가: 종료 코드, 종료 사유, 시그널, 소요 시간, 경고 수. | `supervisor_observed` |
| **저장소 관찰 변경** | agentrec | 실행 전에 고정한 커밋과 실행 후 워크트리의 차이를, agentrec이 직접 측정한 것. | `observed during run, not causal proof` |
| **검증 관찰 결과** | agentrec | 제공자가 멈춘 뒤 agentrec이 저장소 자신의 고정된 검사를 실행했을 때 그 검사가 어떻게 끝났는가. 작업이 어떻게 수행되었는지에 대해서는 아무것도 말하지 않는다. | `verification_observed` |

제공자 진행 상황, 협업 대기, 할 일 목록 수명 주기만 담은 이벤트는 스트림
메타데이터다. 이들은 어떤 액션도 지칭하지 않으며, 경고 수를 부풀리지 않는다.

## 빠른 시작

**사전 요구 사항.** 소스에서 빌드하려면 Go 1.26 이상이 필요하다. 지원되는 제공자
CLI가 이미 `PATH`에 있어야 한다. agentrec은 그것을 실행할 뿐, 설치하지 않는다.
지원 범위를 벗어난 버전은 이벤트 스트림이 여전히 맞을 것이라는 가정 아래
기록되지 않고, 거부된다.

| 제공자 | 실행 파일 | 지원 범위 | 비고 |
|---|---|---|---|
| Claude Code | `claude` | `>=2.1.0, <3.0.0` | `-p`/`--print`가 필요하다. agentrec이 `--output-format stream-json --verbose --include-hook-events`를 주입한다. |
| Codex | `codex` | `>=0.144.0, <1.0.0` | `exec`가 첫 번째 인자여야 한다. agentrec이 `--json`을 주입한다. |

각 태그 릴리스에는 네 개의 아카이브 — `darwin_amd64`, `darwin_arm64`,
`linux_amd64`, `linux_arm64` — 와 넷 모두를 포함하는 `SHA256SUMS` 파일이 있다.
아카이브를 풀면 `agentrec`, `LICENSE`, `THIRD_PARTY_NOTICES.md`,
`third_party/licenses/Apache-2.0.txt`를 담은 디렉터리 하나가 나온다.

```bash
# From a release archive — download SHA256SUMS plus the archive for your platform.
archive=agentrec_0.1.0_darwin_arm64.tar.gz
awk -v file="$archive" '$2 == file { print }' SHA256SUMS | shasum -a 256 -c -
tar -xzf "$archive"
./agentrec_0.1.0_darwin_arm64/agentrec version

# On Linux, use `sha256sum -c -` instead of `shasum -a 256 -c -`.
# Or from source
go install github.com/seongwoo-choi/agentrec/cmd/agentrec@v0.1.0
```

`agentrec version`(이와 동등하게 `agentrec --version`)은 세 줄을 출력한다. 버전,
빌드에 사용된 커밋, 그리고 UTC 빌드 시각이다. 릴리스 바이너리는 태그, 전체 커밋
SHA, RFC 3339 타임스탬프를 담는다. 그 외의 방법으로 만든 빌드는 `dev`, `unknown`,
`unknown`을 보고하므로, 스탬프가 찍히지 않은 바이너리가 릴리스된 것으로 오인되는
일은 없다.

**검증 설정을 커밋한다.** 실행은 저장소가 이미 가지고 있던 검사에 대해서만
검증된다. `.agentrec.example.yaml`을 `.agentrec.yaml`로 복사하고 커밋한다. 각
명령은 셸 없이 직접 실행되므로, 인자는 인자일 뿐 그 이상이 아니다:

```yaml
version: 1
verify:
  - name: go-test
    command: ["go", "test", "./...", "-count=1", "-timeout=420s"]
    timeout: 8m
  - name: go-vet
    command: ["go", "vet", "./..."]
    timeout: 5m
```

**실행을 기록한다.** 작업 디렉터리는 커밋되지 않은 변경이 없고 진행 중인 작업도
없는 Git 체크아웃이어야 한다. 그래야 그 실행 자신의 변경을 구별해 낼 수 있다.
저장소당 한 번에 하나의 실행만 가능하다. 두 번째 실행은 큐에 쌓이지 않고
거부된다.

```bash
# Claude Code
agentrec trace claude -- -p "add a regression test for the parser"
agentrec trace claude --verify -- -p "add a regression test for the parser"

# Codex
agentrec trace codex --verify -- exec "add a regression test for the parser"

# Record the same task with both agents, from one commit
agentrec shadow run task.md --runner claude --runner codex

# Read runs back
agentrec list
agentrec list --cwd /Users/you/code/agentrec
agentrec show 20260728T093159.858622000Z-582ee874
agentrec show latest

# Report the build this binary came from
agentrec version
```

`agentrec list`는 실행을 최신순으로 출력하며, `PROJECT` 열은 매니페스트가 기록한
작업 디렉터리의 마지막 요소에서 가져온다. 절대 경로가 아닌 것을 담은 매니페스트는
추측하는 대신 `unknown`을 보고한다.

`--cwd`는 접두사가 아니라 **정확히 한 디렉터리**에 매칭된다. 주어진 경로는 절대
경로로 만들어져 정규화되고, 매니페스트 자신의 작업 디렉터리 — 이 또한 절대
경로이며 같은 방식으로 정규화된다 — 가 정확히 그것일 때만 해당 실행이 남는다.
하위 디렉터리는 다른 경로이며, 심볼릭 링크를 통해 들어온 또 다른 경로도
마찬가지다.

## 리포트의 모습

`agentrec show`는 읽기 전용이다. 번들에서 실행 하나를 렌더링할 뿐 아무것도 쓰지
않는다. 실제로 기록된 실행에서 발췌한 것이다(`582ee874`, 액션 하나로 줄임):

```
PROVIDER-REPORTED ACTIONS
09:34:32  EDIT  /Users/csw/code/agentrec/README.md
  Source       claude
  Assurance    provider_reported
  Result       success
  Duration     1.128s

SUPERVISOR-OBSERVED RESULT
  Provider     claude
  Version      2.1.220
  Exit Reason  completed
  Exit Code    0
  Duration     2m50.625s
  Warnings     0

REPOSITORY-OBSERVED CHANGES
  Status       AVAILABLE
  Files        1 (1 tracked, 0 untracked)
  Diff         +18/-1, 0 binary
  Stored Text  0
  Baseline     43d37240e960ad2f321276045b2bb8d710f5a4db
  Attribution  observed during run, not causal proof

VERIFICATION-OBSERVED RESULT
  Status       PASS
  Config       .agentrec.yaml
  Config SHA-256 e20695bb3ebee3381b54da6fc46b6b1efa1adc9b87a5eb99b45505b5dbdfae3f
  Check        PASS go-test  "go" "test" "./..." "-count=1" "-timeout=420s"  42.486s  exit 0
  Check        PASS go-vet  "go" "vet" "./..."  348ms  exit 0
  Attribution  verification_observed
```

`agentrec trace`는 무엇이든 출력하기 전에 같은 번들을 같은 방식으로 읽어
`<run>/report.md`에 쓴다. 단 한 번뿐이며 다시 쓰지 않는다. 그 이름으로 이미 서
있는 리포트는 덮어쓰이는 대신 거부된다.

## 하나의 작업으로 두 에이전트 비교하기

`agentrec shadow run`은 하나의 작업을 두 번 — 한 번은 Claude Code로, 한 번은
Codex로 — 단일 커밋 베이스라인에서 기록하고, 기록된 두 실행을 나란히 출력한다:

```bash
agentrec shadow run task.md --runner claude --runner codex
```

각 레그는 소스 저장소의 `HEAD`에서 만들어진 일회용 **분리된 Git 워크트리**에서
기록된다. 위치는 `$AGENTREC_HOME/shadow/<group>/<runner>`이고 모드는 `0700`이며,
해당 레그의 증거가 마감되면 제거된다. 두 레그 모두 평범한 실행 번들을 남기므로,
체크아웃이 사라진 뒤에도 `agentrec list`와 `agentrec show <run-id>`로 다시 읽을 수
있다. 비교 자체는 stdout으로 출력되며, 각 레그의 영속적인 `report.md`는 자기
번들에 남는다.

비교는 러너당 한 블록씩, 항상 이 순서로, 그리고 이 필드들을 이 순서로 출력한다 —
실행 ID, 검사가 어떻게 끝났고 무엇에 고정되어 있었는지, 프로세스가 어떻게
끝났는지, 실행이 자기 체크아웃에 무엇을 남겼는지, 그리고 얼마나 많은 일을 했는지:

```
SHADOW COMPARISON

claude
  Run ID       20260729T101500.000000000Z-1a2b3c4d
  Verification PASS
  Config SHA-256 e20695bb3ebee3381b54da6fc46b6b1efa1adc9b87a5eb99b45505b5dbdfae3f
  Exit Reason  completed
  Exit Code    0
  Duration     2m50.625s
  Repository   AVAILABLE  1 files (1 tracked, 0 untracked)  +18/-1, 0 binary
  Actions      12
  Warnings     0

codex
  ...
```

이 명령이 주는 것과 주지 않는 것:

- **격리는 간섭을 줄일 뿐, 인과적 귀속이 아니다.** 각 레그의 저장소 델타는 여전히
  `observed during run, not causal proof`로 기록된다.
- **점수도, 승자도, 추천도 없다.** 비교는 기록된 필드를 보여줄 뿐, 거기서 파생된
  것은 아무것도 보여주지 않는다. 어느 실행을 선호할지는 읽는 사람의 판단이다.
  제공자가 보고하는 비용과 토큰 필드는 오늘의 기록된 증거에 들어 있지 않으므로,
  비교는 그것들을 보여주지 않는다.
- **바이트 단위 hermetic sandbox가 아니라 Git 체크아웃이다.** 추적되지 않는 `.env`
  파일과 로컬 자격 증명은 레그로 복사되지 않는다. 추적 파일은 사용자의 Git이
  체크아웃하므로 설정된 attribute, filter, hook은 그대로 적용된다. agentrec은 자격
  증명 전달이나 워크스페이스 준비 단계를 추가하지 않는다.
- **준비할 수 없는 저장소는 절반만 준비되는 대신 거부된다.** 커밋된 `.gitmodules`
  또는 커밋된 Git LFS 포인터 파일은 어떤 체크아웃이 생기기도 전에 거절된다.
- **작업은 하나의 명령줄 인자다.** 작업 파일은 한 번만 읽힌다 — 최대 64 KiB의,
  심볼릭 링크가 아닌 일반 UTF-8 파일 하나 — 그리고 각 에이전트에
  `claude -p -- <task>`와 `codex exec --json -- <task>`로 전달된다. stdin으로 주는
  프롬프트나 여러 인자에 나누어 주는 프롬프트는 여기서 지원되지 않는다.
- **검증은 필수이며, 레그는 직렬화된다.** 두 실행 모두 커밋된 `.agentrec.yaml`에
  대해 검증되고 하나가 끝난 뒤 다른 하나가 실행된다. 검사는 겹치지 않지만 변경 가능한
  인증, 캐시, 네트워크 서비스와 그 밖의 외부 상태는 레그 사이에 초기화되지 않는다.
  따라서 입력한 러너 순서가 두 번째 제공자가 관찰하는 상태에 영향을 줄 수 있다.
- **연결된 워크트리는 보안 경계가 아니다.** 공통 Git 디렉터리와 레퍼런스를 공유하며,
  제공자는 소스 체크아웃에 명시적으로 접근할 수 있다. 잠금은 agentrec 프로세스끼리만
  조정한다. agentrec은 각 owned worktree를 제거한 뒤 소스 `HEAD`, 상태, 인덱스,
  레퍼런스, 워크트리 목록, 공통 저장소 config를 사전 점검 snapshot과 비교한다.
  관찰된 drift가 있으면 다음 레그를 시작하지 않고 `1`로 끝내며, 이를 보고할 뿐
  파괴적으로 복구하지 않는다.

종료 코드: 사용법 또는 사전 점검 거부는 `2` — 러너를 두 번 지정하거나 러너가
없는 경우, 읽을 수 없는 작업 파일, 더러운 체크아웃, 커밋되지 않은
`.agentrec.yaml`, 저장소 안에 있는 `AGENTREC_HOME` — 이 모두는 어떤 체크아웃이나
제공자가 존재하기 전에 일어난다. 그다음, 두 레그가 모두 완료되고 두 검증이 모두
통과하면 `0`, 레그가 실패했거나 불완전하게 끝났거나 소스 저장소를 바꿨거나
체크아웃을 제거할 수 없었으면 `1`, 실행이 중단되었으면 `130`이다. **제공자 자신의 종료 코드는 그 번들 안의
증거이며, 집계 명령이 그것을 그대로 내보내는 일은 결코 없다.**

최종 제공자 실행 결정 시점에 이미 hold되었거나 queue된 중단은 그 제공자의 실행을
막는다. 그 userspace 결정 뒤 전달된 중단은 현재 레그의 프로세스 그룹을 멈춘다.
POSIX 시그널 전달과 프로세스 시작은 하나의 atomic 연산이 아니다. agentrec은 그
레그의 증거를 마감하고 체크아웃을 제거하며 다음 레그를 절대 실행하지 않는다. 그러면
비교는 실행되지 않은 러너를 `(not run)`으로 표시한다. agentrec이 곧바로 죽으면 —
`SIGKILL`이거나 머신이 내려가는 경우 — 남겨진 체크아웃은 소스 저장소에서
`git worktree prune`을 실행하고 `$AGENTREC_HOME/shadow` 아래에 남은 디렉터리를
삭제해 복구한다. 오래된 워크트리를 자동으로 수거하는 기능은 없다.

## 실행이 저장되는 위치

`$AGENTREC_HOME`이 설정되어 있으면 `$AGENTREC_HOME/runs` 아래, 그렇지 않으면
`~/.local/share/agentrec/runs` 아래다. 실행 디렉터리는 `0700`으로, 그 안의 모든
파일은 `report.md`를 포함해 `0600`으로 생성된다 — 번들은 비공개 저장소를 인용할 수
있으므로, 그것을 기록한 사용자만 읽을 수 있다. 실행당 하나의 디렉터리가
`manifest.json`, `prompt.txt`, 살균된 이벤트 스트림과 stderr, `actions.jsonl`,
`process/result.json`, `git/`(베이스라인, 결과, 추적되지 않는 파일 본문),
`verification/results.json`, `report.md`를 담는다.

## 상태와 종료 코드

상태는 기록된 그대로 표시되며, 결코 추론되지 않는다:

- **저장소** — `AVAILABLE`(측정됨), `UNAVAILABLE`(측정이 산출되지 않음),
  `PENDING`(실행 전에 기록되었고 끝내 답을 얻지 못함). 개수는 `AVAILABLE`일 때만
  표시된다. `PENDING` 실행의 0은 *측정되지 않음*을 뜻하는 것이지 *아무것도 없다고
  측정됨*을 뜻하지 않는다.
- **검증** — `PASS`, `FAIL`, `TIMEOUT`, `ERROR`, `TAINTED`. 검증을 요청하지 않은
  실행은 `(none)`을 표시하는데, 이는 통과한 검사가 아니다.
- **설정 오염** — `--verify`는 제공자가 시작되기 전에 `.agentrec.yaml`과 그
  SHA-256을 고정한다. 실행이 그 파일을 다시 썼다면 검증은 `TAINTED`, 사유
  `config_changed`로 기록되고, **아무것도 실행되지 않으며**, 고정된 검사들은
  `PENDING`으로 남는다.

종료 코드: `0` 제공자가 완료되었고 검증이 있었다면 통과함, `1`–`125` 제공자
자신의 종료 코드가 그대로 전달됨, `1` 기록·렌더링 또는 검증이 실패함, `2`
agentrec이 잘못 호출됨, `130` 중단됨.

Ctrl-C와 SIGTERM은 모두 도달한 지점에서 곧바로 따르는 대신 붙잡아 둔다. 제공자가
실행되는 동안만이 아니라 기록 전체에 걸쳐 그렇다. agentrec은 제공자의 프로세스
그룹을 멈추고, 매니페스트를 마감하고, 저장소를 측정하고, 고정된 검사를 실행하고,
리포트를 작성한 뒤 `130`으로 종료한다 — 그래서 그 순서 중 어느 지점에서 중단된
실행이든 `PENDING`으로 방치되는 대신 어떻게 끝났는지를 말한다. 처음 온 시그널이
마지막으로 붙잡는 시그널이다. 그 이후 처리는 운영체제로 되돌아가므로, 두 번째
Ctrl-C는 프로세스를 그 자리에서 끝낸다.

`process/result.json`은 프로세스가 종료했을 때는 종료 코드를, 시그널에 의해
죽었을 때는 종료시킨 시그널을 기록한다. 시그널에 의해 죽은 프로세스에는 종료
코드가 없으며, 어느 필드도 다른 필드로부터 추론되지 않는다.

## 보안

- **영속화 전 구조적 가림 처리.** 제공자 이벤트와 stderr는 기록되기 전에 가려진다.
  정규화된 형태가 13개의 비밀 접미사(`TOKEN`, `SECRET`, `PASSWORD`, `APIKEY`,
  `AUTHORIZATION`, `COOKIE`, …) 중 하나로 끝나는 필드 이름 아래의 값, 그리고
  `NAME=VALUE` 할당과 토큰 형태는 `[REDACTED:n]`이 된다. 규칙 버전은 매니페스트마다
  기록된다.
- **추적되지 않는 파일의 본문은 저장된다.** 위치는 `git/untracked/` 아래이고,
  해시는 살균된 텍스트에 대해 계산된다 — 원본 텍스트의 해시는 짧은 비밀을 추측으로
  되돌려줄 수 있기 때문이다.
- **리포트는 원본 제공자 이벤트 스트림, 추적되는 패치, 추적되지 않는 파일 본문을
  결코 담지 않는다.** 리포트는 제공자에서 파생된 정규화된 요약은 담는다. 액션은
  하나의 레이블, 허용 목록에 있는 하나의 상세 필드, 그리고 제어 문자를 이스케이프한
  고정된 요약 필드로 축약되므로, 어떤 제공자 문자열도 타임라인 행을 위조하거나
  터미널을 조종할 수 없다. 번들은 방어적으로 다시 읽히고, 심볼릭 링크는 따라가는
  대신 거부되며, 크기와 줄 길이와 항목 수에는 상한이 있다.
- **가림 처리 횟수가 0이라는 것은 비밀이 없다는 주장이 아니다.** 그것은 어떤 규칙도
  일치하지 않았다는 뜻이다 — 이름 없는 필드 안의 비밀, 할당이 아니라 산문 속의
  비밀, 또는 최소 길이보다 짧은 비밀 역시 같은 0을 만든다.

## 목표가 아닌 것

- **시스템 콜 수준으로 완전하지 않다.** 에이전트가 작업하는 동안 그것을 관찰하는
  것은 아무것도 없다. agentrec은 제공자가 보고한 것, 실행 전후로 저장소가 어떻게
  보였는지, 그리고 그 뒤에 독립적인 검사가 무엇을 말했는지를 기록한다.
- **저장소 델타는 인과적 귀속이 아니다.** 그 변경은 실행 중에 일어났다. 그것은
  에이전트가 그것을 만들었다는 것과 같지 않다. 체크아웃을 편집하는 다른 무엇이든
  같은 델타에 들어오며, 모든 리포트가 그렇게 말한다. 마찬가지로 통과한 검증은 실행이
  남긴 트리에서 고정된 검사가 통과했다는 것만을 말한다.
- **대화형 세션은 기록되지 않으며**, 정책 엔진도, 샌드박스도, 원격 업로드도 없다 —
  agentrec은 관찰하고 로컬에 쓴다.

**지원 범위: macOS와 Linux.** 프로세스 그룹 감독은 `darwin || linux`만을 위해
만들어졌으므로(`internal/runner/process_unix.go`), Windows는 빌드되지 않았고
검증되지도 않았다.

## 증거

동작에 대한 주장은
[docs/dogfood/2026-07-28-evidence.md](docs/dogfood/2026-07-28-evidence.md)가
뒷받침한다 — 고정된 20회 시도 체크포인트와 그에 이은 실제 변경들로, 검증 `FAIL`,
제공자 비정상 종료, 설정 `TAINTED`, 중단, 그리고 그 실행들이 확립하지 **않는**
것까지 다룬다.

`agentrec shadow run`의 실제 제공자 성공 경로는
[docs/dogfood/2026-07-29-shadow-evidence.md](docs/dogfood/2026-07-29-shadow-evidence.md)가
뒷받침한다. macOS에서 같은 커밋으로 Claude Code와 Codex를 한 번 실행했고, 두
고정 검사가 모두 통과했으며, 두 worktree는 제거되고 두 번들은 보존됐다. 그 실행은
실제 제공자의 실패·중단 경로나 Linux 런타임을 확립하지 않는다. 해당 lifecycle
경로는 통제된 대역을 사용하는 저장소 테스트가 다룬다.

## 개발

```bash
go test ./... -count=1 -timeout=420s
go test -race ./... -count=1 -timeout=600s
go vet ./...
gofmt -l .
go build ./...

# Build the release archives locally; publishes nothing.
# The output directory must not already exist.
scripts/build-release.sh v0.1.0 "$(git rev-parse HEAD)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" dist
```

`.github/workflows/release.yml`은 `v*.*.*` 태그에서 같은 스크립트를 실행하고, 모든
아카이브의 목록과 자신이 빌드한 바이너리의 버전 출력을 확인한 뒤에야 게시한다.
이미 존재하는 릴리스에 대해서는 실행을 거부한다.

## 라이선스

agentrec은 [MIT 라이선스](LICENSE)로 제공된다. 서드파티 표기와 의존성 라이선스는
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)에 보존되어 있다.
