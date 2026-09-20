<p align="center">
  <a href="assets/agentrec-wordmark.svg"><img src="assets/agentrec-wordmark.svg" alt="agentrec — 코딩 에이전트를 위한 플라이트 레코더" width="100%"></a>
</p>

<table align="center">
  <tr>
    <td width="50%" align="center">
      <a href="assets/viewer-ko-light.png"><img src="assets/viewer-ko-light.png" alt="agentrec viewer: 기록된 세션을 도구 호출이 딸린 대화로, 여섯 개의 증거 타일과 증거 인스펙터와 함께 읽는 모습"></a><br>
      <sub><b>실행 하나를 다시 읽기.</b><br>에이전트가 말한 것, 프로세스가 한 것, 저장소가 보여주는 것, 체크가 돌려준 것을 서로 섞지 않고 보여줍니다.</sub>
    </td>
    <td width="50%" align="center">
      <a href="assets/agentrec-evidence-layers.svg"><img src="assets/agentrec-evidence-layers.svg" alt="agentrec 번들의 네 가지 증거 계층"></a><br>
      <sub><b>관측자 넷, 출처 표기 넷.</b><br>어떤 것도 점수로 합치지 않고, 없는 증거를 통과로 바꾸지 않습니다.</sub>
    </td>
  </tr>
</table>

# agentrec

<div align="center">

[English](README.md) | 한국어 | [日本語](README.ja.md) | [简体中文](README.zh-CN.md)

[![CI](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml/badge.svg)](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/seongwoo-choi/agentrec?logo=github)](https://github.com/seongwoo-choi/agentrec/releases)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/seongwoo-choi/agentrec?style=flat&logo=github)](https://github.com/seongwoo-choi/agentrec)

</div>

<p align="center">
  <strong>코딩 에이전트의 모든 실행이, 터미널이 사라진 뒤에도 읽을 수 있는 출처 명시 로컬 증거 번들을 남깁니다.</strong><br>
  <em>agentrec이 직접 실행했든, 대화형 세션을 기록했든 마찬가지입니다. provider의 주장, 프로세스 결과, 저장소 변경, 고정된 체크 — 각각 다른 관측자에게서 오고, 점수로 합쳐지지 않습니다.</em>
</p>

**agentrec**은 Claude Code 또는 Codex 실행 한 번을 번들로 기록합니다. 정규화된
액션 타임라인, 감독한 프로세스의 결과, 실행 구간 동안의 저장소 차이, 그리고
저장소 스스로 고정해 둔 체크의 결과. 각각은 서로 다른 관측자에게서 오고, 번들은
이를 섞지 않습니다. 그래서 코드 리뷰, 장애 조사, 인수인계, 새 에이전트 버전을
믿을지에 대한 판단이 요약이 아니라 관측된 사실에서 출발합니다.

[릴리스 노트](docs/releases/v0.15.2.md) ·
[설계 노트](docs/plans/2026-07-27-agentrec-flight-recorder.md) ·
[Shadow runner 설계](docs/plans/2026-07-29-shadow-runner.md) ·
[Dogfood 증거](docs/dogfood/2026-07-28-evidence.md) ·
[서드파티 고지](THIRD_PARTY_NOTICES.md)

> [!NOTE]
> agentrec은 실시간 에이전트 frontend도, 클라우드 텔레메트리 서비스도, 관찰된
> 모든 파일 변경을 에이전트가 일으켰다는 증명도 아닙니다. 실행 하나를 둘러싼
> 로컬 증거 경계입니다. 무엇이, 누구에 의해 관측됐고, 무엇을 확정할 수 없는지를
> 말하기 때문에 쓸모가 있습니다.

## 빠른 시작

**설치.** Homebrew가 가장 쉽고, 체크섬이 있는 아카이브나 `go install`도 됩니다.

```sh
brew install seongwoo-choi/tap/agentrec
agentrec version
```

```sh
archive=agentrec_0.15.2_darwin_arm64.tar.gz
awk -v file="$archive" '$2 == file { print }' SHA256SUMS | shasum -a 256 -c -
tar -xzf "$archive"
./agentrec_0.15.2_darwin_arm64/agentrec version
```

```sh
go install github.com/seongwoo-choi/agentrec/cmd/agentrec@v0.15.2
```

릴리스마다 `darwin_amd64`, `darwin_arm64`, `linux_amd64`, `linux_arm64` 아카이브와
이를 모두 담은 `SHA256SUMS` 하나가 딸려 옵니다. `agentrec version`은 태그·커밋·UTC
빌드 시각을 찍고, 그 밖의 방법으로 만든 빌드는 `dev`라고 답합니다. 소스 빌드에는
Go 1.26 이상, `shadow run`에는 Git 2.36 이상이 필요합니다. 설치본이 여럿일 수 있으면
`agentrec version --verbose`가 실제로 실행된 파일과 `PATH` 위의 모든 `agentrec`을
알려줍니다.

**실행을 검증할 체크를 고정**하려면 `.agentrec.yaml`을 커밋하세요
(`.agentrec.example.yaml`을 복사). 각 명령은 셸 없이 직접 실행됩니다.

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

**agentrec이 직접 띄우는 실행을 기록.** 작업 디렉터리는 깨끗한 Git 체크아웃이어야
하고, 저장소당 한 번에 하나만 기록합니다.

```sh
agentrec trace claude -- -p "add a regression test for the parser"
agentrec trace claude --verify -- -p "add a regression test for the parser"
agentrec trace claude --timeout 30m -- -p "add a regression test for the parser"
agentrec trace codex --verify -- exec "add a regression test for the parser"
agentrec trace claude --verify --allow-unsupported-version -- -p "..."
```

**이미 쓰고 있는 대화형 세션을 기록.** `setup`이 provider 훅을 설치합니다(사용자
파일 또는 프로젝트 파일, 기존 훅 유지, 옆에 백업 작성, 다시 실행해도 변화 없음).
이후 여는 모든 세션이 실행으로 기록됩니다. Codex는 새 훅을 신뢰하도록 Codex 안에서
`/hooks`를 한 번 실행해야 합니다.

```sh
agentrec setup
agentrec setup --claude --verify
agentrec setup --codex --project
agentrec hooks print --claude
```

**다시 읽기.** `start`는 뷰어를 `http://127.0.0.1:7788/`에 백그라운드로 띄워 두고,
`view`는 포그라운드로 띄우며, `list`·`show`·`events`는 같은 번들을 터미널에서
읽습니다.

```sh
agentrec start
agentrec status
agentrec stop
agentrec view latest
agentrec list
agentrec show latest
agentrec events latest --json
```

## 뷰어

<table align="center">
  <tr>
    <td width="50%" align="center">
      <a href="assets/viewer-ko-dark.png"><img src="assets/viewer-ko-dark.png" alt="다크 모드의 agentrec 뷰어"></a><br>
      <sub><b><code>agentrec view</code>.</b> 읽기 전용, 루프백 전용, 외부 자원 없음.</sub>
    </td>
    <td width="50%" align="center">
      <a href="assets/agentrec-evidence-layers.svg"><img src="assets/agentrec-evidence-layers.svg" alt="네 가지 증거 계층"></a><br>
      <sub><b>같은 번들, 네 계층.</b> 모든 요약은 변하지 않는 기록 위의 접기·라벨·위치입니다.</sub>
    </td>
  </tr>
</table>

- **실행 목록** — 제목이 먼저 오는 행에 provider·프로젝트·시각·소요 시간과, 따로
  표시되는 프로세스·검증 판정. 검색, 프로젝트 선택, 접힌 고급 필터, 그리고 불러온
  실행을 provider·검증 결과·프로젝트별로 세는 접힌 집계.
- **실행 상세** — 요청과 에이전트의 마지막 메시지를 나란히. 프로세스·검증·저장소·경고
  4장 요약. 실패한 실행의 실패 triage. **Verify now**, 또는 `--allow-run` 없이 띄웠다면
  나중에 검증하는 명령.
- **타임라인** — **읽기 보기**는 일상적인 도구 액션을 접고 프롬프트(`나 · 3 / 12`),
  응답, 편집, 실패, 알 수 없는 상태는 그대로 보여줍니다. **변경**은 파일을 디렉터리별로
  묶고, **프로바이더 이벤트**는 `PostToolUse`와 훅 수명주기 기록을 접습니다. **전체
  액션/파일/이벤트**는 토글 하나 거리이고, 모든 행은 인스펙터에서 원본 기록을 엽니다.
- **실행을 가로질러** — 모든 실행에서 단어를 검색해 해당 액션이나 변경 파일에 바로
  도착. 두 실행을 나란히 비교. 정확한 행으로 가는 로컬 증거 링크 복사.
- **라이브** — 아직 진행 중인 실행은 페이지가 스스로 갱신되고, 지금의 작업 트리를
  보여줍니다.

## 네 가지 증거 계층

| 계층 | 관측자 | 의미 | 기록되는 출처 |
| --- | --- | --- | --- |
| 🗣️ **Provider가 보고한 액션** | 에이전트 | 에이전트가 했다고 말한 것 — 도구 호출, 셸 명령, 파일 읽기·편집, MCP 호출, Codex 파일 변경. 정규화·요약하되 증명으로 삼지 않습니다. | `provider_reported` |
| 👁️ **감독자가 관측한 결과** | agentrec | provider 프로세스가 어떻게 끝났는가: 종료 코드, 종료 사유, 시그널, 소요 시간, 경고 수. agentrec이 띄우지 않은 세션은 `NOT OBSERVED`. | `supervisor_observed` |
| 🌳 **저장소에서 관측한 변경** | agentrec | 실행 전에 고정한 커밋과 실행 후 작업 트리의 차이. agentrec이 직접 측정합니다. | `observed during run, not causal proof` |
| ✅ **검증에서 관측한 결과** | agentrec | provider가 멈춘 뒤 agentrec이 저장소가 고정해 둔 체크를 돌렸을 때의 결과. 일이 어떻게 이뤄졌는지는 말하지 않습니다. | `verification_observed` |

## 두 가지 기록 방식

| | 🚀 `agentrec trace` | 🎧 대화형 세션 |
| --- | --- | --- |
| provider를 띄우는 주체 | agentrec, 부모 프로세스로서 | 평소처럼 사용자. provider의 훅이 agentrec에 보고 |
| 감독자가 관측한 결과 | 종료 코드, 시그널, 소요 시간 | `NOT OBSERVED`. `Ended By`가 `SessionEnd` 훅이 끝을 알렸는지, 레코더가 포기했는지(`session_lost`, 훅 없이 8시간) 말함 |
| 기준선 | 프로세스 시작 전에 고정 | `SessionStart` 훅이 도착할 때 고정 |
| 체크아웃 상태 | 깨끗해야 하고 저장소당 실행 하나 | 더러운 체크아웃과 동시 세션도 거부하지 않고 기록 |
| 검증 | `--verify`가 실행 전에 `.agentrec.yaml`을 고정 | `--verify`로 출력한 fragment에서만, 그리고 `.agentrec.yaml`이 추적되며 `HEAD`와 같을 때만 |

Codex는 `PostToolUseFailure`를 보내지 않으므로 실패한 명령은 응답에 실패가 적힌
완료 액션으로 나타나고, `apply_patch` 편집은 패치 헤더에 파일을 적습니다. 세션이
비활성화한 훅은 공백을 남기지, 없었던 것이 되지 않습니다.

## 명령

| 명령 | 하는 일 |
| --- | --- |
| 🚀 `agentrec trace <claude\|codex> [--verify] [--allow-unsupported-version] [--timeout <d>] -- <args...>` | agentrec이 띄우고 감독하는 비대화형 실행 하나를 기록합니다. |
| 🧩 `agentrec setup [--claude] [--codex] [--verify] [--project] [--uninstall]` | 대화형 세션을 기록하는 훅을 설치합니다. 플래그가 없으면 물어봅니다. |
| ▶️ `agentrec start [--listen <loopback-address>] [--no-open] [--allow-run]` | 뷰어를 백그라운드로 띄웁니다. `--allow-run`이면 페이지에서 비교와 나중 검증을 시작할 수 있습니다. |
| ⏹️ `agentrec stop` · ℹ️ `agentrec status` | 백그라운드 뷰어를 멈춤 · 뷰어, 실행 수, 훅 설치 여부를 보고. |
| 🖥️ `agentrec view [<run-id>\|latest] [--listen <loopback-address>] [--no-open] [--allow-run]` | 읽기 전용 뷰어를 포그라운드로 띄웁니다. |
| 📋 `agentrec list [--cwd <path>] [--exit-reason <reason>] [--verification-status <status>] [--failures-only] [--json]` | 최신순으로 실행을 나열합니다. `--json`은 스키마 버전이 붙습니다. |
| 📄 `agentrec show <run-id>\|latest [--failures-only] [--json]` | 번들에서 실행 하나를 렌더링합니다. 아무것도 쓰지 않습니다. |
| 🗂️ `agentrec changes <run-id>\|latest [--json]` | 패치나 파일 내용 없이 변경 파일 목록(최대 250개)을 나열합니다. |
| 🧾 `agentrec events <run-id>\|latest [--json]` | 기록된 provider 이벤트를 요약하거나 덤프합니다. |
| ✅ `agentrec verify <run-id>\|latest` | 커밋된 체크를 지금의 저장소에 대해 다시 돌리고, 결과를 실행 옆에 나중 측정으로 기록합니다. |
| 🗑️ `agentrec trash [restore <run-id> \| empty \| sweep <age>]` | 뷰어에서 삭제한 실행을 나열·복원·삭제·정리합니다. |
| 🎧 `agentrec hooks print --claude\|--codex [--verify]` | `setup`이 설치할 훅 fragment를 출력합니다. |
| ⚖️ `agentrec shadow run <task-file> --runner claude --runner codex` · `shadow show <group-id>` | 한 작업을 같은 커밋 기준선에서 격리된 worktree로 두 번 기록 · 비교를 다시 렌더링. |
| 🏷️ `agentrec version [--verbose]` | 태그·커밋·UTC 빌드 시각을 찍습니다. `--verbose`는 `PATH` 위의 모든 `agentrec`을 나열합니다. |

모든 명령이 `-h`/`--help`를 받습니다. `agentrec hook <provider>`와 `agentrec session
serve`도 있지만, 앞의 것은 provider가, 뒤의 것은 첫 훅이 실행합니다.

## 리포트는 이렇게 생겼습니다

`agentrec show`는 번들에서 실행을 렌더링하고 아무것도 쓰지 않습니다. 실제 실행에서
액션 하나만 남긴 발췌:

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

`agentrec trace`는 같은 내용을 `<run>/report.md`에 한 번만 씁니다. 그 이름에 이미
리포트가 있으면 덮어쓰지 않고 거부합니다.

## 두 에이전트를 한 작업으로 비교하기

```sh
agentrec shadow run task.md --runner claude --runner codex
agentrec shadow show <group-id>
```

한 작업을 Claude Code로 한 번, Codex로 한 번, 같은 커밋 기준선에서
`$AGENTREC_HOME/shadow/<group>/` 아래 일회용 분리 worktree에 각각 기록합니다. 두
다리 모두 보통의 실행 번들을 남깁니다.

| 주는 것 | 주지 않는 것 |
| --- | --- |
| 같은 커밋에서 나온 실행 둘, 같은 커밋된 `.agentrec.yaml`로 검증 | 점수, 승자, 추천 — 판단은 읽는 사람의 몫 |
| 두 다리 사이의 간섭을 줄이는 격리. 한 다리 뒤 소스가 바뀌면 다음 다리를 멈춤 | 인과 귀속 — 각 변경은 여전히 `observed during run, not causal proof` |
| 아무것도 만들기 전 거부는 종료 `2`, 다리는 `0`/`1`, 중단은 `130` | 샌드박스 — 연결된 worktree는 공통 Git 디렉터리를 공유하고, 추적되지 않는 `.env`는 복사되지 않음 |

## 주장보다 증거

상태는 기록된 그대로 보여주고 추론하지 않습니다:

| 표시 | 의미 |
| --- | --- |
| `AVAILABLE` | 저장소를 측정했습니다. 개수는 여기서만 보여줍니다. |
| `NOT RUN` | 검증을 요청하지 않았습니다. 중립이며 통과가 아닙니다. |
| `NOT OBSERVED` | 감독한 프로세스가 없습니다: agentrec이 띄우지 않은 세션. |
| `NOT RECORDED` | 저장소를 측정하지 않았습니다. 중립이며 통과가 아닙니다. |
| `PENDING` | 실행 전에 쓰였고 답을 받지 못했습니다. 0은 *측정 안 함*을 뜻합니다. |
| `PASS` / `FAIL` / `TIMEOUT` / `ERROR` | 실행이 남긴 트리에서 고정된 체크가 어떻게 끝났는가. |
| `TAINTED` | 실행이 고정 후 `.agentrec.yaml`을 고쳤습니다: **아무것도 실행되지 않았습니다**. |
| `completed` / `nonzero` / `timeout` / `interrupted` | agentrec이 본 감독 프로세스의 끝. |
| `session_ended` / `session_lost` / `running` / `unknown` | `SessionEnd` 훅이 끝을 알림 — 또는 레코더가 기다림을 멈춤, 아직 기다리는 중, 어떻게 끝났는지 쓰지 못하고 끝남. |

| 종료 코드 | 의미 |
| --- | --- |
| `0` | provider가 완료했고 검증이 있었다면 통과했습니다. |
| `1`–`125` | `trace`가 그대로 넘긴 provider 자신의 종료 코드. |
| `1` | 기록, 렌더링 또는 검증 실패. |
| `2` | agentrec을 잘못 호출했습니다. |
| `130` | 중단됨 — provider 그룹을 멈추고, 저장소를 측정하고, 체크를 돌리고, 리포트를 쓴 뒤입니다. |

agentrec이 주장하지 않는 것:

- **syscall 수준으로 완전하지 않습니다.** 기록은 provider가 보고한 것, 실행 전후의
  저장소, 그리고 그 뒤 독립 체크가 말한 것입니다.
- **저장소 변경은 인과 귀속이 아닙니다.** 체크아웃을 건드린 다른 무엇이든 같은 변경에
  들어가고, 모든 리포트가 그렇게 말합니다.
- **세션의 끝은 provider의 말입니다.** 누가 실행을 끝냈는지 리포트가 말합니다.
- **정책 엔진도, 샌드박스도, 원격 업로드도 없습니다.** macOS와 Linux를 지원하고
  Windows는 빌드도 검증도 되지 않았습니다.

## 보안

- **뷰어는 브라우저가 아니라 머신을 신뢰합니다.** 인증 없이 루프백에서 듣기 때문에
  로컬 프로세스는 모든 실행을 읽고 휴지통으로 옮길 수 있습니다. 다른 origin의 페이지는
  못 합니다: 삭제에는 뷰어 페이지만 읽을 수 있는 토큰이 필요합니다. 지우는 것은
  `agentrec trash empty`뿐입니다. `--allow-run`이면 로컬 프로세스가 당신 권한으로
  `agentrec shadow run`도 띄울 수 있으니, 원하지 않으면 플래그를 끄세요.
- **저장 전 구조적 비식별화.** 17개 비밀 필드 접미사(`TOKEN`, `SECRET`, `PASSWORD`,
  `APIKEY`, `COOKIE`, …) 아래의 값, `NAME=VALUE` 대입, 13개 벤더 토큰 형태가
  `[REDACTED:n]`이 됩니다. 비식별화 0건은 비밀이 없다는 주장이 아닙니다.
- **리포트는 원본 이벤트 스트림, 추적 패치, 미추적 파일 본문을 담지 않습니다.** 액션은
  라벨과 허용된 필드로 줄이고 제어 문자를 이스케이프하며, 번들은 방어적으로 읽습니다
  (심볼릭 링크 거부, 크기 제한).
- **저장소 증거는 Git 기본값에 고정**되어 저장소 속성이나 운영자 설정이 패치를 바꿀 수
  없습니다.
- **릴리스 아카이브는 체크섬만 있고 서명은 없습니다.** `SHA256SUMS`는 산출물의 동일성을
  보장하지, 배포자의 신원을 보장하지 않습니다.

## 실행이 저장되는 위치

`$AGENTREC_HOME/runs`, 없으면 `~/.local/share/agentrec/runs`. 디렉터리는 `0700`,
파일은 `0600`. 실행마다 한 디렉터리에 `manifest.json`, `prompt.txt`, 비식별화된
이벤트 스트림과 stderr, `actions.jsonl`, `process/result.json`(trace 실행),
`git/`, `verification/results.json`, `report.md`가 있습니다. 삭제한 실행은 `trash/`에서
기다립니다. `AGENTREC_HOME`은 기록하는 저장소 바깥에 있어야 합니다.

## 문서

- [릴리스 노트](docs/releases/) — 릴리스마다 파일 하나, 최신은 [v0.15.2](docs/releases/v0.15.2.md)
- [Flight recorder 설계](docs/plans/2026-07-27-agentrec-flight-recorder.md) · [Shadow runner 설계](docs/plans/2026-07-29-shadow-runner.md)
- [Dogfood 증거 — 레코더](docs/dogfood/2026-07-28-evidence.md) · [shadow run](docs/dogfood/2026-07-29-shadow-evidence.md)
- [뷰어 설계 계약](DESIGN.md) · [서드파티 고지](THIRD_PARTY_NOTICES.md)

## 개발

```sh
npm ci --include=dev
npm run test:ui
go test ./... -count=1 -timeout=420s
go test -race ./... -count=1 -timeout=600s
go vet ./...
gofmt -l .
go build ./...
scripts/build-release.sh v0.15.2 "$(git rev-parse HEAD)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" dist
```

`scripts/build-release.sh`는 아카이브를 로컬에서 만들 뿐 아무것도 배포하지 않습니다.
`release.yml`은 `v*.*.*` 태그에서 같은 스크립트를 돌려 모든 아카이브를 점검한 뒤에만
배포하며, 이미 있는 릴리스는 덮어쓰지 않습니다. Homebrew tap은 릴리스마다 실제
`brew install`과 `brew test`로 검증한 뒤 formula를 갱신합니다.

## 번역 유지 관리

`README.md`가 정본입니다. 현지화된 README는 단어 대 단어 번역이 아니라 그 언어
독자를 위해 쓰되, 모든 명령·링크·지원 버전 범위·안전 주의를 그대로 유지합니다.
검사기는 자동화로 증명할 수 있는 것만 봅니다: 헤딩 구조, 실행 가능한 코드 블록,
외부 링크.

```sh
python3 scripts/check-readme-localizations.py
sh scripts/check-readme-localizations_test.sh
```

## 라이선스

agentrec은 [MIT 라이선스](LICENSE)로 제공됩니다. 서드파티 저작자 표시와 의존성
라이선스는 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)에 보존됩니다.
