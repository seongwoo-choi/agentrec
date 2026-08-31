<div align="center">

# agentrec

**코딩 에이전트를 위한 플라이트 레코더. 터미널이 사라진 뒤에도 읽을 수 있는, 출처가 명시된 로컬 증거 번들을 모든 실행에 남깁니다.**

[![CI](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml/badge.svg)](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[English](README.md) | 한국어 | [日本語](README.ja.md) | [简体中文](README.zh-CN.md)

</div>

## 문제

코딩 에이전트가 작업을 마치고 나면 스크롤백만 남습니다. 스크롤백은 금세 밀려나고,
에이전트가 했다고 *말한* 일과 실제로 일어난 일을 섞어 버리며, 저장소가 여전히
빌드되는지는 알려 주지 않습니다.

agentrec은 비대화형 Claude Code 또는 Codex 실행 한 번을 번들로 기록합니다. 번들에는
정규화된 액션 타임라인, 감독한 프로세스의 결과, 실행 구간 전후 저장소의 차이,
그리고 저장소가 직접 고정한 검사의 결과가 담깁니다. 각각은 서로 다른 관찰자가
기록한 것이며, 번들은 이들을 구분해 보관합니다.

## agentrec을 써야 하는 이유

에이전트 실행이 일회성 터미널 세션을 넘어 코드 리뷰, 장애 조사, 인수인계, 또는 새
에이전트나 제공자 버전을 신뢰할지 결정하는 근거가 될 때 agentrec을 사용하세요.

- **요약을 믿지 않고 검토합니다.** `report.md`는 제공자가 보고한 액션, 프로세스 결과,
  측정된 저장소 델타, 실제로 실행된 검사를 구분합니다. 검토자는 주장, 변경, 검증을
  각각 독립적으로 확인할 수 있습니다.
- **실패했거나 의심스러운 실행을 나중에 분석합니다.** 번들은 종료 사유, stderr 문맥,
  경고, 파싱되지 않은 provider stdout, 실행 중 관찰된 저장소 상태를 보존합니다.
  스크롤백이 사라진 뒤에도 timeout, parser mismatch, non-zero exit, 예상치 못한 diff를
  진단할 수 있습니다.
- **인수인계를 재현 가능하게 만듭니다.** 시작 커밋과 검증 설정을 고정하고, 그 검사가
  반환한 결과를 기록합니다. 다음 엔지니어는 누군가의 기억이나 설명 대신 오래 남는
  산출물과 명령을 받습니다.
- **승자를 억지로 정하지 않고 에이전트를 비교합니다.** `shadow run`은 하나의
  baseline에서 Claude와 Codex에 각각 별도의 worktree와 evidence bundle을 제공합니다.
  기록된 사실만 보여 줄 뿐, 액션 수, diff, 검사 결과를 근거 없는 점수로 바꾸지
  않습니다.
- **제공자 업그레이드를 보수적으로 처리합니다.** 지원하지 않는 provider version은
  기본적으로 거부됩니다. 명시적으로 override하면 manifest와 report에
  `versionUnverified` 표시가 남으므로, parser 위험이 있는 timeline이 나중에 완전히
  이해된 증거로 오인되지 않습니다.

agentrec은 에이전트를 실시간으로 조작하는 대화형 frontend나 클라우드 텔레메트리 서비스가 아닙니다. 또한 에이전트가
관찰된 모든 파일 변경을 일으켰다는 증명도 아닙니다. 비대화형 실행 한 번을 둘러싼 로컬
증거 경계로서, 누가 무엇을 관찰했는지와 무엇을 확정할 수 없는지를 함께 남깁니다.

## 번역본 관리

`README.md`는 사실관계의 기준 문서입니다. 각 언어 README는 단어 단위로 옮기지 말고
독자에게 자연스러운 기술 문서로 다시 써야 합니다. 다만 명령, 링크, 지원 버전 범위와
모든 귀속·안전 관련 주의사항은 반드시 보존해야 합니다.

자연스러운 문장인지는 해당 언어를 아는 사람이 검토해야 합니다.
`scripts/check-readme-localizations.py`는 자동으로 증명할 수 있는 계약만 검사합니다.
제목 구조, 실행 가능한 코드 블록의 내용, 외부 링크 대상을 확인하지만 번역의 의미가
보존됐는지까지 보장하지는 않습니다.

```bash
python3 scripts/check-readme-localizations.py
sh scripts/check-readme-localizations_test.sh
```

## 네 가지 증거 계층

| 계층 | 관찰자 | 의미 | 기록되는 귀속 |
|---|---|---|---|
| **제공자 보고 액션** | 에이전트 | 에이전트가 했다고 말한 일입니다. 툴 호출, 셸 명령, 파일 읽기와 편집, MCP 호출, Codex 파일 변경이 해당합니다. 정규화하고 요약하지만, 증명으로 취급하지는 않습니다. | `provider_reported` |
| **감독자 관찰 결과** | agentrec | 제공자 프로세스가 어떻게 종료됐는지입니다. 종료 코드, 종료 사유, 시그널, 소요 시간, 경고 수를 담습니다. | `supervisor_observed` |
| **저장소 관찰 변경** | agentrec | 실행 전에 고정한 커밋과 실행 후 worktree의 차이를 agentrec이 직접 측정한 것입니다. | `observed during run, not causal proof` |
| **검증 관찰 결과** | agentrec | 제공자가 멈춘 뒤 agentrec이 저장소가 고정해 둔 검사를 실행했을 때 그 검사가 어떻게 끝났는지입니다. 작업이 어떻게 수행됐는지는 알려 주지 않습니다. | `verification_observed` |

제공자 진행 상황, 협업 대기, 할 일 목록 수명 주기만 담은 이벤트는 스트림
메타데이터입니다. 이들은 어떤 액션도 가리키지 않으며 경고 수를 늘리지 않습니다.

제공자 이벤트가 아닌 stdout 한 줄, 즉 업데이트 배너나 지원 중단 경고처럼 에이전트
CLI가 이벤트 스트림 옆에 출력하는 내용은 `provider-stdout.unparsed.log`에 보관합니다.
다른 모든 내용과 마찬가지로 가림 처리하고, manifest에는 `unparsedLines`로 집계하며,
report에도 명시합니다. 이벤트 사이에 끼워 넣지 않고 실행을 실패로 처리하지도
않습니다. 산문 한 줄을 출력한 제공자도 실제로 실행된 것이며, 그 기록을 버리면
agentrec이 보존하려는 증거를 훼손하게 되기 때문입니다.

## 빠른 시작

**사전 요구 사항.** 소스에서 빌드하려면 Go 1.26 이상이 필요합니다. `agentrec shadow run`은 `git worktree list --porcelain -z`를 사용하므로 Git 2.36 이상도 필요하지만, `trace`에는 이 Git 하한이 없습니다. 지원되는 제공자
CLI는 이미 `PATH`에 있어야 합니다. agentrec은 실행만 하며 설치하지 않습니다.
지원 범위를 벗어난 버전은 이벤트 스트림이 여전히 맞을 것이라는 가정으로 기록하지
않고 거부합니다. `agentrec trace --allow-unsupported-version`은 이 거부를 무시합니다.
실행은 기록되고 manifest에는 `versionUnverified`가 표시되며, 모든 report가 그 사실을
알립니다. 즉, timeline은 해당 버전의 스트림을 이해한다고 주장하지 않는 parser가
읽은 것이고, 나머지 세 증거 계층은 parser에 전혀 의존하지 않습니다. `agentrec shadow
run`에는 이런 override가 없습니다. 제대로 읽은 timeline과 그렇지 않은 timeline을
비교하는 것은 비교가 아니기 때문입니다.

| 제공자 | 실행 파일 | 지원 범위 | 비고 |
|---|---|---|---|
| Claude Code | `claude` | `>=2.1.0, <3.0.0` | `-p`/`--print`가 필요합니다. agentrec이 `--output-format stream-json --verbose --include-hook-events`를 주입합니다. |
| Codex | `codex` | `>=0.144.0, <1.0.0` | `exec`가 첫 번째 인자여야 합니다. agentrec이 `--json`을 주입합니다. |

태그된 릴리스마다 `darwin_amd64`, `darwin_arm64`, `linux_amd64`, `linux_arm64`의 네
아카이브와, 네 개 모두를 포함하는 `SHA256SUMS` 파일을 제공합니다. 아카이브를 풀면
`agentrec`, `LICENSE`, `THIRD_PARTY_NOTICES.md`,
`third_party/licenses/Apache-2.0.txt`가 들어 있는 디렉터리 하나가 생성됩니다.

```bash
# Homebrew
brew install seongwoo-choi/tap/agentrec
agentrec version

# From a release archive — download SHA256SUMS plus the archive for your platform.
archive=agentrec_0.2.0_darwin_arm64.tar.gz
awk -v file="$archive" '$2 == file { print }' SHA256SUMS | shasum -a 256 -c -
tar -xzf "$archive"
./agentrec_0.2.0_darwin_arm64/agentrec version

# On Linux, use `sha256sum -c -` instead of `shasum -a 256 -c -`.
# Or from source
go install github.com/seongwoo-choi/agentrec/cmd/agentrec@v0.2.0
```

`agentrec version`(`agentrec --version`도 동일함)은 세 줄을 출력합니다. 버전,
빌드에 사용한 커밋, UTC 빌드 시각입니다. 릴리스 바이너리에는 태그, 전체 커밋 SHA,
RFC 3339 타임스탬프가 들어 있습니다. 그 밖의 방법으로 만든 빌드는 `dev`, `unknown`,
`unknown`을 보고하므로, 스탬프가 없는 바이너리를 릴리스된 바이너리로 오인하지
않습니다.

최신 태그 릴리스는 `v0.2.0`입니다. `shadow run`, `events`, 읽기 전용 viewer,
Change Explorer, Unified Overview, same-path-observed correlation을 포함합니다.

**검증 설정을 커밋하세요.** 실행은 저장소가 이미 보유한 검사에 대해서만 검증됩니다.
`.agentrec.example.yaml`을 `.agentrec.yaml`로 복사해 커밋하세요. 각 명령은 셸을 거치지
않고 직접 실행되므로, 인자는 그저 인자일 뿐입니다.

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

**실행을 기록하세요.** 작업 디렉터리는 커밋되지 않은 변경이나 진행 중인 작업이 없는
Git 체크아웃이어야 합니다. 그래야 실행 자체가 만든 변경을 구분할 수 있습니다.
저장소당 동시에 실행할 수 있는 작업은 하나뿐입니다. 두 번째 실행은 큐에 넣지 않고
거부합니다.

```bash
# Claude Code
agentrec trace claude -- -p "add a regression test for the parser"
agentrec trace claude --verify -- -p "add a regression test for the parser"
agentrec trace claude --timeout 30m -- -p "add a regression test for the parser"

# Codex
agentrec trace codex --verify -- exec "add a regression test for the parser"

# 이 파서가 대상으로 쓰이지 않은 제공자 버전에 대해 기록한다
agentrec trace claude --verify --allow-unsupported-version -- -p "..."

# Record the same task with both agents, from one commit
agentrec shadow run task.md --runner claude --runner codex

# Read runs back
agentrec list
agentrec list --cwd /Users/you/code/agentrec
agentrec list --exit-reason nonzero
agentrec list --verification-status FAIL
agentrec list --cwd /Users/you/code/agentrec --exit-reason timeout
agentrec show 20260728T093159.858622000Z-582ee874
agentrec show latest
agentrec events 20260728T093159.858622000Z-582ee874
agentrec events latest --json

# Open the local read-only Action Timeline
agentrec view latest
agentrec view 20260728T093159.858622000Z-582ee874

# Report the build this binary came from
agentrec version
```

`agentrec list`는 실행을 최신순으로 출력합니다. `PROJECT` 열은 manifest가 기록한 작업
디렉터리의 마지막 요소에서 가져옵니다. 절대 경로가 아닌 값이 들어 있는 manifest는
짐작하는 대신 `unknown`을 보고합니다.

`--cwd`는 접두사가 아니라 **정확히 하나의 디렉터리**와 일치합니다. 입력한 경로는
절대 경로로 만들고 정리하며, manifest의 작업 디렉터리도 절대 경로로 같은 방식으로
정리한 뒤 정확히 일치할 때만 실행을 남깁니다. 하위 디렉터리는 다른 경로이고,
심볼릭 링크를 통해 들어간 경로도 마찬가지입니다.

`--exit-reason`은 `EXIT` 열에 표시되는 기록값과 정확히 일치하는 실행만 남깁니다.
서로 다른 결과를 임의의 실패 범주로 묶지 않습니다. `--cwd`와 어느 순서로든 함께
사용할 수 있습니다. 일치하는 실행이 없으면 `No runs.`를 출력하고 종료 코드 `0`을
반환합니다.

`VERIFICATION` 열은 `PASS`, `FAIL`, 대문자로 표시한 기록 상태, 또는 verification
artifact가 없는 실행의 `UNAVAILABLE`을 보여 줍니다. `--verification-status`는 이
터미널 안전 표시값과 정확히 일치합니다. 세 filter는 어느 순서로든 조합할 수 있으며,
임의의 non-passing 범주는 만들지 않습니다.

`agentrec events <run-id>|latest`는 선택적인 sanitized provider-event JSONL
artifact를 읽습니다. 사람용 출력은 `provider_reported` 귀속, 이벤트 수, 정렬된 최상위
`type` 개수만 보여 주며 중첩된 provider payload는 렌더링하지 않습니다. `--json`은 설명
문구 없이 안정적인 wrapper
`{"schemaVersion":1,"runId":...,"attribution":"provider_reported","artifactPresent":...,"events":[...]}`만
출력합니다. artifact가 없는 이전 bundle은 `artifactPresent: false`와 빈 이벤트 목록으로
보고합니다. 두 모드 모두 심볼릭 링크와 일반 파일이 아닌 항목을 거부하고, 파일 크기·줄
길이·이벤트 수·JSON token 수·중첩 깊이에 상한을 적용하며, JSONL의 각 줄이 정확히 하나의
JSON object인지 검증합니다. 사람용 type 이름은 collision 없이 terminal-safe하게 quote하고,
JSON 모드는 검증된 sanitized object를 valid JSON으로 보존합니다. 이벤트를 action으로
변환하거나, 점수를 매기거나, provider를 비교하거나, 인과관계의 증거로 취급하지 않습니다.

`agentrec view [<run-id>|latest]`는 크기 제한을 적용하는 동일한 번들 판독 경로 위에서 로컬
읽기 전용 웹 UI를 엽니다. 실행 목록, 기록된 요청, 정규화된 작업 타임라인, 정제된 제공자 이벤트
스트림, 그리고 귀속을 분리한 프로세스·저장소·사용량·검증 증거를 한 화면에서 볼 수 있습니다.
요약 영역에는 프로세스 결과와 소요 시간, 검증 판정, 검증된 저장소 상태와 변경 합계,
작업·이벤트 수, 경고 수가 함께 표시됩니다.
`Changes` 탭에서는 추적·미추적 경로를 확인하고, 추적 경로에 기록된 크기 제한·정제 적용 패치를
열 수 있습니다. 저장소 증거는 실행 중 관찰된 사실일 뿐 인과관계의 증명은 아니라고 명시합니다.
파일 작업의 명시적 정규화 입력 경로가 표시된 변경 경로와 정확히 일치하면
`same path observed — not causal proof`로 표시합니다. 명령어나 결과 텍스트에서 경로를 추정하지 않습니다.
recorder는 실행 당시 파일시스템 namespace가 남아 있을 때 명시적 경로를 저장소 상대
`repositoryPaths`로 변환합니다. 따라서 viewer가 live 저장소를 다시 열지 않아도 symlink alias를 처리합니다.
상위 작업 ID(`parentId`)가 있는 하위 에이전트 작업은 들여쓰기로 표현하지만, 제공자가 보고한
관계를 OS가 관찰한 인과관계라고 주장하지 않습니다. 작업이나 이벤트를 선택하면 정제된
`input`/`result`를, 변경 파일을 선택하면 저장소 메타데이터와 크기 제한 패치를 확인할 수 있습니다.
모든 payload는 실행 가능한 HTML이 아닌 텍스트로 표시됩니다.

각 뷰어 스냅샷은 하나의 실행 디렉터리를 고정하고 작업·이벤트 스트림과 tracked patch의 불변 바이트
사본을 만들며, 색인된 Changes 문서를 검증합니다. 생성 중 표시 대상 artifact가 바뀌면 스냅샷을
거부합니다. API는 작업·이벤트·변경 목록을 페이지당 최대 250개, 목표 1 MiB로 제공하고 patch
페이지는 1 MiB로 제한합니다. 정상 레코드 하나가 스트림 목표 크기를 넘을 수는 있지만 최대 크기의
번들 전체를 한 번에 전송하거나 렌더링하지는 않습니다.

기본값은 `127.0.0.1`의 임의 포트에 연결한 뒤 기본 브라우저를 여는 것입니다. 브라우저를 열지
않고 URL만 출력하려면 `--no-open`, 고정 루프백 포트를 쓰려면 `--listen
127.0.0.1:<port>`를 지정하세요. 루프백이 아닌 주소는 거부하며, 뷰어에는 데이터를 변경하는
엔드포인트나 외부 자원이 없습니다.

## 리포트의 모습

`agentrec show`는 읽기 전용입니다. 번들에서 실행 하나를 렌더링할 뿐 아무것도 쓰지
않습니다. 실제로 기록된 실행(`582ee874`, 액션 하나만 남기고 축약)에서 발췌한
예시입니다.

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
`<run>/report.md`에 씁니다. 한 번만 쓰며 다시 쓰지 않습니다. 그 이름의 report가 이미
있으면 덮어쓰지 않고 거부합니다.

## 하나의 작업으로 두 에이전트 비교하기

`agentrec shadow run`은 하나의 작업을 단일 커밋 baseline에서 두 번 기록합니다. 한 번은
Claude Code로, 한 번은 Codex로 실행한 뒤 기록된 두 실행을 나란히 출력합니다.

```bash
agentrec shadow run task.md --runner claude --runner codex
```

각 leg는 소스 저장소의 `HEAD`에서 만든 일회용 **분리된 Git worktree**에서 기록됩니다.
위치는 `$AGENTREC_HOME/shadow/<group>/workspaces/<runner>`이고 모드는 `0700`입니다.
해당 leg의 증거 기록을 마치면 제거합니다. 이후 private
`$AGENTREC_HOME/shadow/<group>/group.json`에는 baseline, 기록된 leg 실행 순서, run ID,
종료 outcome만 남으며 raw task body는 저장하지 않습니다. 두 leg 모두 일반 실행 번들을
남기므로 체크아웃이 사라진 뒤에도 `agentrec list`와 `agentrec show <run-id>`로 다시 읽을 수
있습니다. 비교 결과는 stdout으로 출력하며, 각 leg의 영속적인 `report.md`는 자체 bundle에
남습니다. 나중에 동일한 evidence-only comparison을 다시 출력하려면 다음을 사용합니다.

```bash
agentrec shadow show <group-id>
```

비교는 runner별로 한 블록씩 출력합니다. 블록과 필드는 항상 다음 순서입니다. 실행 ID,
검사가 어떻게 끝났고 무엇에 고정됐는지, 프로세스가 어떻게 끝났는지, 실행이 자신의
체크아웃에 무엇을 남겼는지, 그리고 얼마나 많은 작업을 했는지입니다.

```
SHADOW COMPARISON

claude
  Run ID       20260729T101500.000000000Z-1a2b3c4d
  Order        1
  Verification PASS
  Config SHA-256 e20695bb3ebee3381b54da6fc46b6b1efa1adc9b87a5eb99b45505b5dbdfae3f
  Exit Reason  completed
  Exit Code    0
  Duration     2m50.625s
  Repository   AVAILABLE  1 files (1 tracked, 0 untracked)  +18/-1, 0 binary
  Actions      12
  Warnings     0
  Unparsed     0

codex
  ...

The legs ran in the Order shown, one after another. Provider authentication,
caches, rate limits and any network service both agents use are not reset
between them, so a later leg may observe what an earlier one left.
```

`Order`는 각 leg가 실제로 실행된 순서이며, 블록을 출력하는 순서와는 다릅니다. 두
운영자가 같은 비교를 읽도록 runner 블록은 항상 `claude` 다음 `codex` 순으로
렌더링합니다. 이 고정된 순서만으로는 실제 실행 순서가 가려지므로 `Order`를 함께 표시합니다.

이 명령이 제공하는 것과 제공하지 않는 것:

- **격리는 간섭을 줄일 뿐, 인과적 귀속은 아닙니다.** 각 leg의 저장소 델타는 여전히
  `observed during run, not causal proof`로 기록됩니다.
- **점수, 승자, 추천은 없습니다.** 비교는 기록된 필드만 보여 주며, 그로부터 파생한
  결과는 보여 주지 않습니다. 어느 실행을 선호할지는 읽는 사람의 판단입니다. 제공자가
  보고한 사용량은 각 leg의 provider 및 `run` 또는 `session` 범위와 함께 독립적으로
  표시하며, provider 간 값을 합치거나 동등한 수치로 취급하지 않습니다.
- **바이트 단위로 hermetic한 sandbox가 아니라 Git 체크아웃입니다.** 추적되지 않는
  `.env` 파일과 로컬 자격 증명은 leg로 복사하지 않습니다. 추적 파일은 사용자의 Git이
  체크아웃하므로 설정된 attribute, filter, hook은 계속 적용됩니다. agentrec은 자격
  증명 전달이나 workspace 준비 단계를 추가하지 않습니다.
- **준비할 수 없는 저장소는 반쯤 준비하는 대신 거부합니다.** 커밋된 `.gitmodules`나
  커밋된 Git LFS pointer 파일은 어떤 체크아웃도 만들기 전에 거부합니다.
- **작업은 명령줄 인자 하나입니다.** 작업 파일은 최대 64 KiB의 일반 UTF-8 파일 하나로,
  심볼릭 링크가 아니어야 하며 한 번만 읽습니다. 각 에이전트에는
  `claude -p -- <task>`와 `codex exec --json -- <task>`로 전달합니다. stdin으로 준
  prompt나 여러 인자에 나눈 prompt는 여기서 지원하지 않습니다.
- **검증은 필수이고 leg는 직렬로 실행됩니다.** 두 실행 모두 커밋된 `.agentrec.yaml`을
  기준으로 검증하며, 하나가 끝난 뒤 다른 하나를 실행합니다. 검사는 겹치지 않지만
  변경 가능한 인증 정보, cache, network service 등 외부 상태는 leg 사이에 초기화하지
  않습니다. 따라서 입력한 runner 순서가 두 번째 제공자가 관찰하는 상태에 영향을 줄 수
  있습니다. 비교는 각 leg의 `Order`를 보여 주고 이 사실을 명시하므로, 두 결과를
  동일한 조건에서 나온 것처럼 읽지 않습니다.
- **연결된 worktree는 보안 경계가 아닙니다.** 공통 Git 디렉터리와 refs를 공유하고,
  제공자는 소스 체크아웃에 명시적으로 접근할 수 있습니다. 잠금은 agentrec 프로세스
  사이에서만 조정됩니다. agentrec은 각 owned worktree를 제거한 뒤 소스 `HEAD`, 상태,
  index, refs, worktree 목록, 공통 저장소 config를 사전 점검 snapshot과 비교합니다.
  관찰된 drift가 있으면 다음 leg를 시작하지 않고 `1`로 종료합니다. 단, 실행도
  중단됐다면 `130`이 우선합니다. agentrec은 drift를 보고할 뿐 파괴적으로 복구하지
  않습니다.

종료 코드: 사용법 또는 사전 점검 거부는 `2`입니다. runner를 두 번 지정했거나
지정하지 않은 경우, 읽을 수 없는 작업 파일, 더러운 체크아웃, 커밋하지 않은
`.agentrec.yaml`, 저장소 안에 있는 `AGENTREC_HOME`이 여기에 해당하며, 모두 어떤
체크아웃이나 제공자도 생기기 전에 발생합니다. 이후 두 leg가 모두 완료되고 두 검증이
모두 통과하면 `0`, leg가 실패했거나 불완전하게 끝났거나 소스 저장소를 변경했거나
체크아웃을 제거하지 못했으면 `1`, drift도 함께 관찰됐더라도 실행이 중단됐으면
`130`입니다. **제공자의 종료 코드는 해당 bundle 안의 증거일 뿐이며, 집계 명령이 이를
그대로 전달하지는 않습니다.**

최종 제공자 실행 여부를 결정하는 시점에 이미 보류되었거나 큐에 들어간 interrupt는
해당 제공자의 실행을 막습니다. 그 userspace 결정 뒤에 전달된 interrupt는 현재 leg의
프로세스 그룹을 멈춥니다. POSIX 시그널 전달과 프로세스 시작은 하나의 atomic 연산이
아닙니다. agentrec은 그 leg의 증거를 마감하고 체크아웃을 제거한 뒤 다음 leg는 절대
실행하지 않습니다. 그러면 비교에서 실행하지 않은 runner는 `(not run)`으로 표시합니다.
agentrec이 즉시 종료되면, 예를 들어 `SIGKILL`을 받거나 머신이 꺼지면, 소스 저장소에서
`git worktree prune`을 실행하고 `$AGENTREC_HOME/shadow` 아래 남은 디렉터리를 삭제해
남겨진 체크아웃을 복구합니다. 오래된 worktree를 자동으로 수거하지는 않습니다.

## 실행이 저장되는 위치

`$AGENTREC_HOME`이 설정돼 있으면 `$AGENTREC_HOME/runs` 아래에, 그렇지 않으면
`~/.local/share/agentrec/runs` 아래에 저장합니다. 실행 디렉터리는 `0700`으로 만들고,
그 안의 모든 파일은 `report.md`를 포함해 `0600`으로 만듭니다. bundle이 비공개 저장소를
인용할 수 있으므로, 기록한 사용자만 읽을 수 있습니다. 실행마다 디렉터리 하나가
`manifest.json`, `prompt.txt`, 살균된 이벤트 스트림과 stderr, `actions.jsonl`,
`process/result.json`, `git/`(baseline, 결과, 추적되지 않는 파일 본문),
`verification/results.json`, `report.md`를 담습니다. 제공자가 stdout에 이벤트가 아닌
내용을 출력한 경우에만 `provider-stdout.unparsed.log`도 생성합니다. 이벤트만 출력한
실행에, 그렇지 않은 척하는 빈 파일을 남기지는 않습니다.

## 상태와 종료 코드

상태는 기록된 값을 그대로 표시하며 추론하지 않습니다.

- **저장소**: `AVAILABLE`(측정됨), `UNAVAILABLE`(측정 결과가 생성되지 않음),
  `PENDING`(실행 전에 기록됐으나 끝내 결과를 받지 못함)입니다. 개수는 `AVAILABLE`일
  때만 표시합니다. `PENDING` 실행의 0은 *측정하지 않음*을 뜻하며, *없음을 측정함*을
  뜻하지는 않습니다.
- **검증**: `PASS`, `FAIL`, `TIMEOUT`, `ERROR`, `TAINTED`입니다. 검증을 요청하지 않은
  실행은 `(none)`을 표시하며, 이는 통과한 검사가 아닙니다.
- **설정 오염**: `--verify`는 제공자가 시작하기 전에 `.agentrec.yaml`과 그 SHA-256을
  고정합니다. 실행 중 해당 파일을 다시 썼다면 검증은 `TAINTED`, 사유
  `config_changed`로 기록되고, **아무것도 실행하지 않으며**, 고정한 검사는 `PENDING`으로
  남습니다.

종료 코드는 다음과 같습니다. `0`은 제공자가 완료됐고 검증이 있었다면 통과했음을,
`1`–`125`는 제공자 자체의 종료 코드가 그대로 전달됐음을, `1`은 기록, 렌더링 또는
검증 실패를, `2`는 agentrec을 잘못 호출했음을, `130`은 중단됐음을 뜻합니다.

`--timeout <duration>`은 `30s`, `5m`, `2h` 같은 양의 Go duration을 받으며 제공자
프로세스만 제한합니다. 기한이 지나면 제공자 프로세스 그룹에 SIGTERM을 보내고 고정된
5초 grace를 기다린 뒤, 여전히 실행 중이면 SIGKILL을 보냅니다. 번들은 종료 사유
`timeout`으로 마감되고 agentrec은 `1`로 종료하며, 기한 전에 나온 제공자 이벤트는 그대로
보존됩니다. flag를 생략하면 기존처럼 Ctrl-C 또는 SIGTERM으로 제어하는 무제한 제공자
실행입니다. 저장소 측정, 검증 검사, report 작성은 각각의 자체 제한을 사용하며 이 제공자
timeout에 포함되지 않습니다.

Ctrl-C와 SIGTERM은 전달된 지점에서 즉시 따르지 않고 전체 기록 과정에서 붙잡아 둡니다.
제공자가 실행 중일 때만 해당하는 것이 아닙니다. agentrec은 제공자의 프로세스 그룹을
멈추고, manifest를 마감하고, 저장소를 측정하고, 고정한 검사를 실행하고, report를 쓴
뒤 `130`으로 종료합니다. 따라서 이 순서의 어느 지점에서 중단돼도 실행은 `PENDING`에
방치되지 않고 어떻게 끝났는지를 기록합니다. 첫 번째 시그널이 마지막으로 붙잡는
시그널입니다. 이후 처리는 운영체제로 되돌아가므로 두 번째 Ctrl-C는 프로세스를 그
자리에서 끝냅니다.

`process/result.json`은 프로세스가 종료했을 때는 종료 코드를, 시그널로 종료됐을 때는
종료시킨 시그널을 기록합니다. 시그널로 죽은 프로세스에는 종료 코드가 없으며, 어느
필드도 다른 필드에서 추론하지 않습니다.

## 보안

- **영속화 전에 구조적으로 가림 처리합니다.** 제공자 이벤트, stderr, 이벤트가 아닌
  stdout 줄은 기록하기 전에 가립니다. 정규화된 형태가 17개의 비밀 suffix(`TOKEN`,
  `SECRET`, `PASSWORD`, `APIKEY`, `PASSPHRASE`, `AUTHORIZATION`, `COOKIE`, …) 중 하나로
  끝나는 field name 아래의 값과, `NAME=VALUE` 할당 및 13개 vendor token 형식(GitHub,
  OpenAI, AWS, Google, Stripe, JWT, Slack token과 webhook, GitLab, npm, Hugging Face,
  PyPI)은 `[REDACTED:n]`이 됩니다. 부분 문자열이 아닌 suffix로 일치시키므로
  `PUBLIC_KEY`, `primaryKey`, `token_id`는 읽을 수 있습니다. 규칙 버전은 manifest마다
  기록합니다. `1`과 `2`가 표시된 bundle은 서로 다른 규칙으로 판단한 것이므로 가림
  처리 횟수를 비교할 수 없습니다.
- **추적되지 않는 파일 본문은 저장합니다.** `git/untracked/` 아래에 저장하며, hash는
  살균된 텍스트를 대상으로 계산합니다. 원본 텍스트의 hash는 짧은 비밀을 추측으로
  되돌려줄 수 있기 때문입니다.
- **report에는 원본 제공자 이벤트 스트림, 추적 파일 patch, 추적되지 않는 파일 본문을
  절대 넣지 않습니다.** 제공자에서 파생한 정규화된 요약은 넣습니다. 액션은 label 하나,
  allowlist에 있는 상세 field 하나, 제어 문자를 이스케이프한 고정 summary field로
  축약합니다. 따라서 어떤 제공자 문자열도 timeline 행을 위조하거나 터미널을 조작할 수
  없습니다. bundle은 방어적으로 다시 읽고, 심볼릭 링크는 따라가지 않고 거부하며,
  크기, 줄 길이, 항목 수에는 상한을 둡니다.
- **가림 처리 횟수가 0이라고 해서 비밀이 없다는 뜻은 아닙니다.** 어떤 규칙도 일치하지
  않았다는 뜻일 뿐입니다. 이름 없는 field의 비밀, 할당이 아닌 산문 속 비밀, 최소
  길이보다 짧은 비밀도 모두 같은 0을 만듭니다.

## 목표가 아닌 것

- **시스템 콜 수준에서 완전하지 않습니다.** 에이전트가 작업하는 동안 이를 관찰하는
  기능은 없습니다. agentrec은 제공자가 보고한 내용, 실행 전후 저장소 상태,
  그 뒤 독립적인 검사가 말한 내용을 기록합니다.
- **저장소 델타는 인과적 귀속이 아닙니다.** 변경이 실행 중 발생했다는 사실은
  에이전트가 그 변경을 만들었다는 뜻이 아닙니다. 체크아웃을 편집하는 다른 무엇이든
  같은 델타에 포함되며, 모든 report가 이를 명시합니다. 마찬가지로 검증 통과는 실행이
  남긴 트리에서 고정된 검사가 통과했다는 사실만 뜻합니다.
- **대화형 세션은 기록하지 않습니다.** policy engine, sandbox, remote upload도 없습니다.
  agentrec은 관찰하고 로컬에 기록합니다.

**지원 범위: macOS와 Linux.** Windows는 빌드·검증하지 않았습니다. port에는 프로세스 그룹 감독(`internal/runner/process_unix.go`)뿐 아니라 verification process control(`internal/evidence/verification.go`)과 repository locking(`internal/lock/repository.go`)도 필요합니다.

## 증거

동작에 관한 주장은
[docs/dogfood/2026-07-28-evidence.md](docs/dogfood/2026-07-28-evidence.md)에서
뒷받침합니다. 고정된 20회 시도 checkpoint와 이후 실제 변경을 통해 검증 `FAIL`,
제공자 nonzero 종료, 설정 `TAINTED`, 중단, 그리고 그 실행이 **확립하지 않는**
사항까지 다룹니다.

`agentrec shadow run`의 실제 제공자 성공 경로는
[docs/dogfood/2026-07-29-shadow-evidence.md](docs/dogfood/2026-07-29-shadow-evidence.md)에서
다룹니다. macOS에서 같은 커밋을 기준으로 Claude Code와 Codex를 한 번 실행했고, 두
고정 검사는 모두 통과했으며, 두 worktree는 제거되고 두 bundle은 보존됐습니다. 이
실행은 실제 제공자의 실패 경로, 중단 경로, Linux runtime을 확립하지 않습니다. 해당
lifecycle 경로는 통제된 환경을 쓰는 저장소 테스트로 다룹니다.

## 개발

```bash
go test ./... -count=1 -timeout=420s
go test -race ./... -count=1 -timeout=600s
go vet ./...
gofmt -l .
go build ./...

# Build the release archives locally; publishes nothing.
# The output directory must not already exist.
scripts/build-release.sh v0.2.0 "$(git rev-parse HEAD)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" dist
```

`.github/workflows/release.yml`은 `v*.*.*` 태그에서 같은 스크립트를 실행합니다. 모든
아카이브의 구성 목록과 빌드한 바이너리의 버전 출력을 확인한 뒤에만 게시합니다. 이미
존재하는 릴리스를 대상으로 하면 실행을 거부합니다.

## 라이선스

agentrec은 [MIT 라이선스](LICENSE)로 제공합니다. 서드파티 표기와 의존성 라이선스는
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)에 보존돼 있습니다.
