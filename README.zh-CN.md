<p align="center">
  <a href="assets/agentrec-wordmark.svg"><img src="assets/agentrec-wordmark.svg" alt="agentrec — 面向编码智能体的飞行记录仪" width="100%"></a>
</p>

<table align="center">
  <tr>
    <td width="50%" align="center">
      <a href="assets/viewer-en-light.png"><img src="assets/viewer-en-light.png" alt="agentrec 查看器：把一次已记录的会话当作带工具调用的对话来回读，并配有六个证据卡片和证据检视器"></a><br>
      <sub><b>一次运行，事后回读。</b><br>智能体说了什么、进程做了什么、仓库呈现了什么、检查返回了什么，分开呈现，互不混淆。</sub>
    </td>
    <td width="50%" align="center">
      <a href="assets/agentrec-evidence-layers.svg"><img src="assets/agentrec-evidence-layers.svg" alt="agentrec 证据包的四个证据层"></a><br>
      <sub><b>四个观察者，四种归属。</b><br>不会合并成一个评分，缺失的证据也绝不算作通过。</sub>
    </td>
  </tr>
</table>

# agentrec

<div align="center">

[English](README.md) | [한국어](README.ko.md) | [日本語](README.ja.md) | 简体中文

[![CI](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml/badge.svg)](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/seongwoo-choi/agentrec?logo=github)](https://github.com/seongwoo-choi/agentrec/releases)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/seongwoo-choi/agentrec?style=flat&logo=github)](https://github.com/seongwoo-choi/agentrec)

</div>

<p align="center">
  <strong>每次编码智能体运行都会留下一个带有来源归属的本地证据包，即使终端会话结束后仍可查阅。</strong><br>
  <em>无论是由 agentrec 启动，还是从交互式会话中记录。提供方的主张、进程结果、仓库差异和固定检查——各自来自不同的观察者，绝不合并成一个评分。</em>
</p>

**agentrec** 会将一次 Claude Code 或 Codex 运行记录为一个证据包：规范化的操作时间线、
受监管进程的结果、运行窗口内仓库前后的差异，以及仓库自身固定检查的结果。这些信息由
不同的观察者获得，证据包会将它们明确区分开来。因此，无论是代码审查、事故调查、工作
交接，还是决定是否信任新版智能体，都能从实际观察到的事实出发，而不是从一份摘要出发。

[发布说明](docs/releases/v0.15.3.md) ·
[设计笔记](docs/plans/2026-07-27-agentrec-flight-recorder.md) ·
[Shadow runner 设计](docs/plans/2026-07-29-shadow-runner.md) ·
[Dogfood 证据](docs/dogfood/2026-07-28-evidence.md) ·
[第三方声明](THIRD_PARTY_NOTICES.md)

> [!NOTE]
> agentrec 不是用于实时操控智能体的交互式 frontend，不是云端遥测服务，也不能证明
> 智能体导致了每一处观察到的文件变更。它是围绕一次运行建立的本地证据边界，其价值
> 恰恰在于它会说明观察到了什么、由谁观察到，以及它无法证明什么。

## 快速开始

**安装。** Homebrew 最简单；带校验和的归档包或 `go install` 也可以。

```sh
brew install seongwoo-choi/tap/agentrec
agentrec version
```

```sh
archive=agentrec_0.15.3_darwin_arm64.tar.gz
awk -v file="$archive" '$2 == file { print }' SHA256SUMS | shasum -a 256 -c -
tar -xzf "$archive"
./agentrec_0.15.3_darwin_arm64/agentrec version
```

```sh
go install github.com/seongwoo-choi/agentrec/cmd/agentrec@v0.15.3
```

每个版本附带 `darwin_amd64`、`darwin_arm64`、`linux_amd64`、`linux_arm64` 四个归档包和一个
覆盖全部四者的 `SHA256SUMS`。`agentrec version` 会打印标签、提交和 UTC 构建时间；用其他方式
构建的二进制会报告 `dev`。从源码构建需要 Go 1.26 或更高版本；`shadow run` 还需要 Git 2.36 或
更高版本。如果机器上可能有多个安装，`agentrec version --verbose` 会指出实际执行的文件以及
`PATH` 上的每一个 `agentrec`。

**固定用于验证运行的检查**：提交 `.agentrec.yaml`（复制 `.agentrec.example.yaml`）。每条命令
都直接启动，不经过 shell。

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

**记录由 agentrec 启动的运行。** 工作目录必须是干净的 Git 检出；每个仓库同时只记录一次。

```sh
agentrec trace claude -- -p "add a regression test for the parser"
agentrec trace claude --verify -- -p "add a regression test for the parser"
agentrec trace claude --timeout 30m -- -p "add a regression test for the parser"
agentrec trace codex --verify -- exec "add a regression test for the parser"
agentrec trace claude --verify --allow-unsupported-version -- -p "..."
```

**记录你已经在用的交互式会话。** `setup` 安装提供方钩子（用户文件或项目文件，保留现有钩子，
在旁边写入备份，重复执行不会改变任何东西）；此后打开的每个会话都会作为运行记录下来。Codex
需要在 Codex 内执行一次 `/hooks` 来信任新钩子。

```sh
agentrec setup
agentrec setup --claude --verify
agentrec setup --codex --project
agentrec hooks print --claude
```

**回看。** `start` 在后台把 Viewer 保持在 `http://127.0.0.1:7788/`；`view` 在前台提供同样的
页面；`list`、`show` 和 `events` 在终端中读取同一个证据包。

```sh
agentrec start
agentrec status
agentrec stop
agentrec view latest
agentrec list
agentrec show latest
agentrec events latest --json
```

## Viewer

<table align="center">
  <tr>
    <td width="50%" align="center">
      <a href="assets/viewer-en-dark.png"><img src="assets/viewer-en-dark.png" alt="深色模式下的 agentrec Viewer"></a><br>
      <sub><b><code>agentrec view</code>。</b> 只读、仅回环地址、不加载外部资源。</sub>
    </td>
    <td width="50%" align="center">
      <a href="assets/agentrec-evidence-layers.svg"><img src="assets/agentrec-evidence-layers.svg" alt="四个证据层"></a><br>
      <sub><b>同一个证据包，四个层。</b> 每一处摘要都只是不变记录之上的折叠、标签或位置。</sub>
    </td>
  </tr>
</table>

- **运行列表** — 以标题开头的行，附带提供方、项目、时间、耗时，以及分开显示的进程结论和
  验证结论；搜索、项目选择器和折叠的高级筛选；按提供方、验证结果和项目统计已加载运行的
  折叠计数。
- **运行详情** — 请求与智能体的最后一条消息并排；进程、验证、仓库、警告四张摘要卡；失败运行
  的失败分诊；**Verify now**，或在未带 `--allow-run` 启动时给出稍后验证的命令。
- **时间线** — **阅读视图** 折叠常规工具操作，保留提示（`我 · 3 / 12`）、回复、编辑、失败和
  未知状态可见；**变更** 按目录分组文件；**提供方事件** 折叠 `PostToolUse` 和钩子生命周期
  记录。**全部操作/文件/事件** 只需一次切换，每一行都能在检查器中打开原始记录。
- **跨运行** — 在所有运行中搜索一个词，直达匹配的操作或变更文件；并排比较任意两个运行；复制
  指向精确行的本地证据链接。
- **实时** — 仍在进行的运行会自动刷新页面，并显示当前的工作树。

## 四个证据层

| 层 | 观察者 | 含义 | 记录的归属 |
| --- | --- | --- | --- |
| 🗣️ **提供方报告的操作** | 智能体 | 智能体声称自己做了什么——工具调用、shell 命令、文件读取与编辑、MCP 调用、Codex 文件变更。经过规范化和摘要，但从不视为证明。 | `provider_reported` |
| 👁️ **监管者观察的结果** | agentrec | 提供方进程如何结束：退出码、退出原因、信号、耗时、警告数。对不由 agentrec 启动的会话为 `NOT OBSERVED`。 | `supervisor_observed` |
| 🌳 **仓库观察的变更** | agentrec | 运行前固定的提交与运行后工作树之间的差异，由 agentrec 自行测量。 | `observed during run, not causal proof` |
| ✅ **验证观察的结果** | agentrec | 提供方停止后，agentrec 运行仓库自身固定的检查所得的结果。它不说明工作是如何完成的。 | `verification_observed` |

## 两种记录方式

| | 🚀 `agentrec trace` | 🎧 交互式会话 |
| --- | --- | --- |
| 谁启动提供方 | agentrec，作为父进程 | 一如既往是你；提供方的钩子向 agentrec 报告 |
| 监管者观察的结果 | 退出码、信号、耗时 | `NOT OBSERVED`；`Ended By` 说明是 `SessionEnd` 钩子报告了结束，还是记录器放弃了等待（`session_lost`，八小时无钩子） |
| 基线 | 进程启动前固定 | `SessionStart` 钩子到达时固定 |
| 检出状态 | 必须干净；每个仓库一次运行 | 脏检出和并发会话照样记录，不拒绝 |
| 验证 | `--verify` 在启动前固定 `.agentrec.yaml` | 仅对带 `--verify` 打印的片段，且仅当 `.agentrec.yaml` 已被跟踪并与 `HEAD` 一致时 |

Codex 不发送 `PostToolUseFailure`，因此失败的命令表现为一条响应中写明失败的已完成操作；
其 `apply_patch` 编辑在补丁头中写明文件。会话禁用的钩子留下的是空缺，而不是"不存在"。

## 命令

| 命令 | 作用 |
| --- | --- |
| 🚀 `agentrec trace <claude\|codex> [--verify] [--allow-unsupported-version] [--timeout <d>] -- <args...>` | 记录一次由 agentrec 启动并监管的非交互式运行。 |
| 🧩 `agentrec setup [--claude] [--codex] [--verify] [--project] [--uninstall]` | 安装记录交互式会话的钩子；不带参数时会询问。 |
| ▶️ `agentrec start [--listen <loopback-address>] [--no-open] [--allow-run]` | 在后台启动 Viewer；带 `--allow-run` 时可从页面发起比较和稍后验证。 |
| ⏹️ `agentrec stop` · ℹ️ `agentrec status` | 停止后台 Viewer · 报告 Viewer、运行数以及钩子是否已安装。 |
| 🖥️ `agentrec view [<run-id>\|latest] [--listen <loopback-address>] [--no-open] [--allow-run]` | 在前台提供只读 Viewer。 |
| 📋 `agentrec list [--cwd <path>] [--exit-reason <reason>] [--verification-status <status>] [--failures-only] [--json]` | 按最新顺序列出运行；`--json` 带 schema 版本。 |
| 📄 `agentrec show <run-id>\|latest [--failures-only] [--json]` | 从证据包渲染一次运行。不写入任何内容。 |
| 🗂️ `agentrec changes <run-id>\|latest [--json]` | 列出变更文件清单（最多 250 个），不含补丁或文件内容。 |
| 🧾 `agentrec events <run-id>\|latest [--json]` | 汇总或导出记录的提供方事件。 |
| ✅ `agentrec verify <run-id>\|latest` | 现在针对当前仓库运行已提交的检查，并把结果作为稍后的测量记录在运行旁。 |
| 🗑️ `agentrec trash [restore <run-id> \| empty \| sweep <age>]` | 列出、恢复、清空或清理从 Viewer 删除的运行。 |
| 🎧 `agentrec hooks print --claude\|--codex [--verify]` | 打印 `setup` 将安装的钩子片段。 |
| ⚖️ `agentrec shadow run <task-file> --runner claude --runner codex` · `shadow show <group-id>` | 从同一个已提交基线在隔离的 worktree 中把一个任务记录两次 · 重新渲染比较。 |
| 🏷️ `agentrec version [--verbose]` | 打印标签、提交和 UTC 构建时间；`--verbose` 列出 `PATH` 上的每一个 `agentrec`。 |

每条命令都接受 `-h`/`--help`。`agentrec hook <provider>` 和 `agentrec session serve` 也存在；
前者由提供方运行，后者由第一个钩子启动。

## 报告长什么样

`agentrec show` 从证据包渲染一次运行且不写入任何内容。以下摘自一次真实运行，只保留了一个操作：

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

`agentrec trace` 只把同样的内容写入 `<run>/report.md` 一次；该名称下已有报告时会拒绝而不是
覆盖。

## 用一个任务比较两个智能体

```sh
agentrec shadow run task.md --runner claude --runner codex
agentrec shadow show <group-id>
```

一个任务，用 Claude Code 记录一次、用 Codex 记录一次，从同一个已提交基线出发，各自在
`$AGENTREC_HOME/shadow/<group>/` 下的一次性分离 worktree 中进行。两条腿都留下普通的运行
证据包。

| 它给你的 | 它不给你的 |
| --- | --- |
| 来自同一提交的两次运行，用同一个已提交的 `.agentrec.yaml` 验证 | 分数、赢家或推荐——由读者判断 |
| 减少两条腿之间干扰的隔离；一条腿之后源码漂移会阻止下一条腿 | 因果归属——每个差异仍是 `observed during run, not causal proof` |
| 尚未创建任何内容时拒绝退出 `2`，两条腿为 `0`/`1`，中断为 `130` | 沙箱——链接的 worktree 共享公共 Git 目录，未跟踪的 `.env` 不会被复制进去 |

## 证据先于主张

状态按记录原样显示，从不推断：

| 显示 | 含义 |
| --- | --- |
| `AVAILABLE` | 已测量仓库。计数只在这里显示。 |
| `NOT RUN` | 未请求验证。中性，绝不是通过。 |
| `NOT OBSERVED` | 没有被监管的进程：这是一个不由 agentrec 启动的会话。 |
| `NOT RECORDED` | 未做仓库测量。中性，绝不是通过。 |
| `PENDING` | 运行前写入且从未得到回答。其中的零表示*未测量*。 |
| `PASS` / `FAIL` / `TIMEOUT` / `ERROR` | 固定的检查在运行留下的树上如何结束。 |
| `TAINTED` | 运行在固定之后改写了 `.agentrec.yaml`：**什么都没有执行**。 |
| `completed` / `nonzero` / `timeout` / `interrupted` | agentrec 看到的被监管进程的结束方式。 |
| `session_ended` / `session_lost` / `running` / `unknown` | `SessionEnd` 钩子报告了结束——或记录器停止等待、仍在等待、或结束时未能写下如何结束。 |

| 退出码 | 含义 |
| --- | --- |
| `0` | 提供方完成，且任何验证都通过。 |
| `1`–`125` | 提供方自身的退出码，由 `trace` 原样透传。 |
| `1` | 记录、渲染或验证失败。 |
| `2` | agentrec 被错误调用。 |
| `130` | 被中断——先停止提供方进程组、测量仓库、运行检查、写下报告。 |

agentrec 不主张的事：

- **不是系统调用级完整记录。** 记录是提供方报告的内容、运行前后的仓库状态，以及事后独立检查
  所说的话。
- **仓库差异不是因果归属。** 任何其他编辑检出的东西都会落进同一个差异，每份报告都这么说。
- **会话的结束是提供方的说法。** 报告会说明是谁结束了运行。
- **没有策略引擎、没有沙箱、没有远程上传。** 支持 macOS 和 Linux；Windows 未构建也未验证。

## 安全

- **Viewer 信任的是机器，而不是浏览器。** 它在回环地址上无认证监听，任何本地进程都能读取
  每次运行并把它移到回收站。跨源页面做不到：删除需要只有 Viewer 自己的页面才能读到的令牌。
  只有 `agentrec trash empty` 会真正清除。带 `--allow-run` 时，本地进程还能以你的身份启动
  `agentrec shadow run`——除非你想要这样，否则不要加这个参数。
- **持久化前的结构化脱敏。** 17 个秘密字段后缀（`TOKEN`、`SECRET`、`PASSWORD`、`APIKEY`、
  `COOKIE`……）下的值、`NAME=VALUE` 赋值和 13 种厂商令牌形态会变成 `[REDACTED:n]`。脱敏
  计数为零并不是"没有秘密"的主张。
- **报告从不嵌入原始事件流、跟踪补丁或未跟踪文件内容。** 操作被缩减为标签和允许列表中的
  字段并转义控制字符；证据包以防御方式读取（拒绝符号链接、限制大小）。
- **仓库证据固定在 Git 的默认设置上**，仓库属性和操作者配置无法改写补丁。
- **发布归档带校验和但不签名。** `SHA256SUMS` 证明的是产物一致性，不是发布者身份。

## 运行记录存放在哪里

`$AGENTREC_HOME/runs`，否则为 `~/.local/share/agentrec/runs`；目录权限 `0700`，文件 `0600`。
每次运行一个目录，包含 `manifest.json`、`prompt.txt`、脱敏后的事件流和 stderr、
`actions.jsonl`、`process/result.json`（trace 运行）、`git/`、`verification/results.json` 和
`report.md`。删除的运行在 `trash/` 中等待。`AGENTREC_HOME` 必须位于被记录仓库之外。

## 文档

- [发布说明](docs/releases/) — 每个版本一个文件，最新为 [v0.15.3](docs/releases/v0.15.3.md)
- [Flight recorder 设计](docs/plans/2026-07-27-agentrec-flight-recorder.md) · [Shadow runner 设计](docs/plans/2026-07-29-shadow-runner.md)
- [Dogfood 证据——记录器](docs/dogfood/2026-07-28-evidence.md) · [shadow run](docs/dogfood/2026-07-29-shadow-evidence.md)
- [Viewer 设计契约](DESIGN.md) · [第三方声明](THIRD_PARTY_NOTICES.md)

## 开发

```sh
npm ci --include=dev
npm run test:ui
go test ./... -count=1 -timeout=420s
go test -race ./... -count=1 -timeout=600s
go vet ./...
gofmt -l .
go build ./...
scripts/build-release.sh v0.15.3 "$(git rev-parse HEAD)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" dist
```

`scripts/build-release.sh` 只在本地构建归档，不发布任何东西。`release.yml` 在 `v*.*.*` 标签上
运行同一脚本，检查每个归档后才发布；已存在的版本不会被覆盖。Homebrew tap 在更新 formula 之前
会用真实的 `brew install` 和 `brew test` 验证每个版本。

## 维护翻译

`README.md` 是正本。本地化的 README 面向其读者撰写而非逐字翻译，但保留每条命令、链接、
支持的版本范围和安全提示。检查器只验证自动化能证明的内容：标题结构、可执行代码块和外部链接。

```sh
python3 scripts/check-readme-localizations.py
sh scripts/check-readme-localizations_test.sh
```

## 许可证

agentrec 以 [MIT 许可证](LICENSE) 提供。第三方署名和依赖许可证保留在
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) 中。
