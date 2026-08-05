<div align="center">

# agentrec

**面向编码智能体的飞行记录仪：每次运行都会留下带有来源归属的本地证据包，即使终端会话结束后仍可查阅。**

[![CI](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml/badge.svg)](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[English](README.md) | [한국어](README.ko.md) | [日本語](README.ja.md) | 简体中文

</div>

## 问题

编码智能体完成任务后，终端里通常只剩滚动输出。它很快会被刷掉，既混杂了智能体*声称*做过的事和实际发生的事，也无法说明仓库是否还能正常构建。

agentrec 会将一次非交互式 Claude Code 或 Codex 运行记录为一个证据包，其中包括：规范化的操作时间线、受监管进程的结果、运行期间仓库前后的差异，以及仓库自身固定检查的结果。这些信息由不同的观察者获得，证据包会将它们明确区分开来。

## 为什么要使用 agentrec

当一次智能体运行不只是短暂的终端会话，而是要作为代码审查、事故调查、交接，或评估是否信任新版智能体或提供方的依据时，agentrec 就派得上用场。

- **审查工作时不必相信摘要。** `report.md` 会区分提供方报告的操作、进程结果、实测的仓库差异和实际运行过的检查。审查者可以分别核查主张、变更和验证结果。
- **事后排查失败或可疑的运行。** 证据包会保留退出原因、stderr 上下文、警告、未解析的提供方 stdout，以及运行期间观察到的仓库状态。即使滚动输出已经消失，也能排查超时、解析器不匹配、非零退出或意外 diff。
- **让交接可复现。** 证据包会固定起始提交和验证配置，并记录这些检查的结果。下一位工程师拿到的是可长期保存的产物和命令，而不是某人对自己曾看到什么的转述。
- **比较智能体，但不武断评判高下。** `shadow run` 会基于同一基线，为 Claude 和 Codex 分别创建独立的工作树和证据包。它只呈现记录到的事实，不会把操作数量、diff 或检查结果加工成缺乏依据的评分。
- **谨慎升级提供方。** 默认拒绝不受支持的提供方版本。显式覆盖后，manifest 和报告中会留下 `versionUnverified` 标记，避免日后将存在解析风险的时间线误认为是已被完全理解的证据。

agentrec 不是交互式记录界面，也不是云端遥测服务，更不能证明智能体导致了每一处观察到的文件变更。它为一次非交互式运行建立本地证据边界，同时说明谁观察到了什么，以及它无法证明什么。

## 维护翻译版本

`README.md` 是事实依据的基准文档。各语言 README 不应逐字翻译，而应为当地读者重写成自然的技术文档；但命令、链接、支持版本范围，以及所有归属和安全性注意事项都必须保留。

是否自然仍需由熟悉该语言的人审阅。`scripts/check-readme-localizations.py` 只检查自动化可以证明的契约：标题层级、可执行代码块的内容和外部链接目标。它不能证明翻译仍保留原意。

```bash
python3 scripts/check-readme-localizations.py
sh scripts/check-readme-localizations_test.sh
```

## 四个证据层

| 层 | 观察者 | 含义 | 记录的归属 |
|---|---|---|---|
| **提供方报告的操作** | 智能体 | 智能体声称执行过的操作，包括工具调用、shell 命令、文件读写、MCP 调用和 Codex 文件变更。会被规范化和汇总，但绝不视为证明。 | `provider_reported` |
| **监管方观察到的结果** | agentrec | 提供方进程如何结束：退出码、退出原因、信号、耗时和警告数量。 | `supervisor_observed` |
| **仓库观察到的变更** | agentrec | 运行前固定的提交与运行后工作树之间的差异，由 agentrec 自行测量。 | `observed during run, not causal proof` |
| **验证观察到的结果** | agentrec | 提供方停止后，agentrec 运行仓库自身固定检查时得到的结果。它并不说明工作是如何完成的。 | `verification_observed` |

仅包含提供方进度、协作等待或待办列表生命周期的事件属于流元数据：它们不对应具体操作，也不会增加警告数。

完全不是提供方事件的 stdout 行，例如更新横幅、弃用警告，或智能体 CLI 在事件流旁输出的任何内容，都会保存在 `provider-stdout.unparsed.log` 中，与其他内容一样经过脱敏处理；它会在 manifest 中按 `unparsedLines` 计数，并在报告中注明。这些内容不会混入事件，也不会导致运行失败：即使提供方只额外输出了一行文本，它仍然完成过运行；丢弃这份记录反而会毁掉 agentrec 要保留的证据。

## 快速开始

**前置条件。** 从源码构建需要 Go 1.26 或更高版本。`agentrec shadow run` 使用 `git worktree list --porcelain -z`，还需要 Git 2.36 或更高版本；`trace` 没有这一 Git 版本下限。受支持的提供方 CLI 必须已在 `PATH` 中；agentrec 只负责启动它，不会安装它。超出支持范围的版本会被拒绝，而不是假定其事件流仍然兼容就继续记录。`agentrec trace --allow-unsupported-version` 可以覆盖这一拒绝：运行仍会被记录，manifest 会标记为 `versionUnverified`，每份报告也会说明这一点。时间线由并不声称理解该版本事件流的解析器读取，但其他三层证据完全不依赖该解析器。`agentrec shadow run` 没有这样的覆盖选项，因为无法正确读取的一条时间线不能与正确读取的另一条时间线进行比较。

| 提供方 | 可执行文件 | 支持范围 | 说明 |
|---|---|---|---|
| Claude Code | `claude` | `>=2.1.0, <3.0.0` | 需要 `-p`/`--print`。agentrec 会注入 `--output-format stream-json --verbose --include-hook-events`。 |
| Codex | `codex` | `>=0.144.0, <1.0.0` | `exec` 必须是第一个参数。agentrec 会注入 `--json`。 |

每个已打标签的发布版本都包含四个归档文件：`darwin_amd64`、`darwin_arm64`、`linux_amd64` 和 `linux_arm64`，以及一份覆盖全部四个归档的 `SHA256SUMS` 文件。解压归档后会得到一个目录，内含 `agentrec`、`LICENSE`、`THIRD_PARTY_NOTICES.md` 和 `third_party/licenses/Apache-2.0.txt`。

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

`agentrec version`（等同于 `agentrec --version`）会输出三行：版本、构建所用的提交和 UTC 构建时间。发布版二进制会带有标签、完整提交 SHA 和 RFC 3339 时间戳；其他方式构建的二进制则显示 `dev`、`unknown` 和 `unknown`，因此未加构建信息的二进制不会被误认为发布版。

**提交验证配置。** 一次运行只会依据仓库原本已有的检查进行验证。将 `.agentrec.example.yaml` 复制为 `.agentrec.yaml` 并提交。每条命令都会直接启动，不经 shell，因此参数始终只是参数，不会被当作其他内容解释：

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

**记录一次运行。** 工作目录必须是没有未提交变更、也没有进行中操作的 Git 检出，以便区分本次运行自身造成的变更。每个仓库同一时间只能运行一次；第二次运行会被拒绝，不会排队。

```bash
# Claude Code
agentrec trace claude -- -p "add a regression test for the parser"
agentrec trace claude --verify -- -p "add a regression test for the parser"
agentrec trace claude --timeout 30m -- -p "add a regression test for the parser"

# Codex
agentrec trace codex --verify -- exec "add a regression test for the parser"

# 针对本解析器并非为之编写的提供方版本进行记录
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

# Report the build this binary came from
agentrec version
```

`agentrec list` 会按时间倒序列出运行记录，`PROJECT` 列取自 manifest 所记录工作目录的最后一段路径；若 manifest 中不是绝对路径，则显示 `unknown`，而不是猜测。

`--cwd` 匹配的是**一个完全一致的目录**，而非路径前缀：给定路径会转换为绝对路径并清理；只有 manifest 中的工作目录也为绝对路径、以相同方式清理后与之完全相同，运行记录才会被保留。子目录是不同路径，通过符号链接进入的路径也是不同路径。

`--exit-reason` 只保留与 `EXIT` 列所显示记录值完全一致的运行，不会把不同结果归入一个人为定义的失败类别。它可与 `--cwd` 按任意顺序组合使用。若没有匹配项，则输出 `No runs.` 并以状态码 `0` 退出。

`VERIFICATION` 列显示 `PASS`、`FAIL`、转为大写的已记录状态，或在运行没有 verification artifact 时显示 `UNAVAILABLE`。`--verification-status` 与这个对终端安全的显示值完全匹配。三个过滤条件可按任意顺序组合使用，且不会创建人为定义的 non-passing 类别。

`agentrec events <run-id>|latest` 读取可选的、已清理的提供方事件 JSONL 文件。面向人的输出只显示 `provider_reported` 归属、事件数量以及排序后的顶层 `type` 计数，不呈现嵌套的提供方 payload。`--json` 不输出说明文字，只输出稳定的包装结构 `{"schemaVersion":1,"runId":...,"attribution":"provider_reported","artifactPresent":...,"events":[...]}`。旧 bundle 若没有该文件，会以 `artifactPresent: false` 和空事件列表报告。两种模式都拒绝符号链接和非普通文件，限制文件大小、行长、事件数、JSON token 数和嵌套深度，并要求 JSONL 每行恰好是一个 JSON 对象。面向人的 type 名采用无碰撞且对终端安全的引用形式；JSON 模式则以有效 JSON 保留已验证、已清理的对象。事件不会被转换为 action，不会用于评分或比较提供方，也不会被当作因果证明。

## 报告长什么样

`agentrec show` 是只读操作：它从证据包渲染一次运行，不写入任何内容。以下为一次真实记录运行（`582ee874`）的节选，只保留了一个操作：

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

`agentrec trace` 会在输出任何内容前，将针对同一证据包的同一份读取结果写入 `<run>/report.md`。它只写一次，绝不会再次写入；如果该名称的报告已存在，命令会拒绝执行而非覆盖它。

## 在一个任务上比较两个智能体

`agentrec shadow run` 会从同一个已提交的基线出发，将同一任务分别用 Claude Code 和 Codex 记录一次，并并排输出两次运行的记录：

```bash
agentrec shadow run task.md --runner claude --runner codex
```

每个执行分支都会在一次性的**分离 Git 工作树**中记录。该工作树从源仓库的 `HEAD` 创建，位于 `$AGENTREC_HOME/shadow/<group>/workspaces/<runner>`，权限为 `0700`，并会在该分支的证据收集完成后移除。之后，private 的 `$AGENTREC_HOME/shadow/<group>/group.json` 只保留 baseline、已记录分支的执行顺序、run ID 和终止 outcome，不保存 raw task body。两个分支都会留下普通运行证据包，因此检出目录被删除后，仍可通过 `agentrec list` 和 `agentrec show <run-id>` 查阅。比较结果本身输出到 stdout；每个分支持久化的 `report.md` 则保留在各自的证据包中。要在之后重新输出相同的 evidence-only comparison，请运行：

```bash
agentrec shadow show <group-id>
```

比较会为每个 runner 输出一个区块，且区块和字段的顺序始终固定：运行 ID、检查如何结束及其固定依据、进程如何结束、运行在其检出中留下了什么，以及它执行了多少操作：

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

`Order` 表示各分支实际执行的先后顺序，而不是区块输出的顺序：runner 区块始终按 `claude`、`codex` 的顺序渲染，确保不同操作者看到一致的比较结果；但也正因此，固定的输出顺序会掩盖哪个智能体先执行。

该命令提供什么，以及不提供什么：

- **隔离只能减少相互干扰，不能证明因果归属。** 每个分支的仓库差异仍记录为 `observed during run, not causal proof`。
- **没有评分、胜者或推荐。** 比较只展示记录到的字段，不会从中推导其他结论。应偏好哪次运行由读者自行判断。提供方报告的使用量会按分支独立显示，并明确标注 provider 以及 `run` 或 `session` 范围；不同提供方的数值不会合并，也不会被视为等价。
- **这是 Git 检出，不是字节级的完全隔离沙箱。** 未跟踪的 `.env` 文件和本地凭据不会复制到分支。跟踪文件由操作者的 Git 检出，因此已配置的 attributes、filters 和 hooks 仍会生效。agentrec 不会添加凭据传输或工作区准备步骤。
- **无法准备的仓库会被拒绝，而不是半途准备。** 已提交的 `.gitmodules` 或 Git LFS 指针文件会在任何检出创建前被拒绝。
- **任务只作为一个命令行参数传递。** 任务文件只读取一次，必须是一个不超过 64 KiB、非符号链接的普通 UTF-8 文件；随后分别以 `claude -p -- <task>` 和 `codex exec --json -- <task>` 传给智能体。这里不支持从标准输入传入提示词，也不支持将提示词拆成多个参数。
- **验证是强制的，两个分支串行运行。** 两次运行都会依据已提交的 `.agentrec.yaml` 验证，并且依次执行。它们的检查不会重叠，但可变的认证状态、缓存、网络服务和其他外部状态不会在两个分支之间重置；因此输入的 runner 顺序可能影响第二个提供方观察到的状态。比较会显示每个分支的 `Order` 并说明这一点，避免将两次结果视为在完全相同条件下产生。
- **链接工作树不是安全边界。** 它与源仓库共享 Git 公共目录和引用，提供方也可以明确访问源检出。锁只用于协调 agentrec 进程。agentrec 移除每个自有工作树后，会将源仓库的 `HEAD`、状态、索引、引用、工作树列表和公共仓库配置与预检快照比较。若观察到漂移，后续分支不会启动，并以 `1` 退出；若运行同时被中断，则 `130` 优先。agentrec 会报告漂移，但不会进行破坏性恢复。

退出码如下：用法或预检拒绝为 `2`，包括重复指定或未指定 runner、任务文件不可读、检出不干净、`.agentrec.yaml` 未提交，或 `AGENTREC_HOME` 位于仓库内；这些情况都发生在创建任何检出或启动任何提供方之前。两条分支均完成且两次验证均通过时为 `0`；任一分支失败、未完成、修改源仓库，或其检出无法移除时为 `1`；运行被中断时为 `130`，即使同时观察到漂移也是如此。**提供方自身的退出码会作为证据保留在其证据包中，聚合命令绝不会将它透传出去。**

如果在最终决定启动提供方时已经收到或排队了中断信号，该提供方不会启动。若信号在作出该用户态决定后送达，则会停止当前分支的进程组；POSIX 信号投递和进程启动并不是原子操作。agentrec 会完成该分支的证据收集、移除检出，且不会启动下一个分支；比较结果中未运行的 runner 会显示为 `(not run)`。如果 agentrec 被直接杀死，例如收到 `SIGKILL` 或机器关机，可在源仓库中运行 `git worktree prune`，再删除 `$AGENTREC_HOME/shadow` 下遗留的目录来恢复遗留检出。不会自动清理陈旧工作树。

## 运行记录存放在哪里

设置 `$AGENTREC_HOME` 时，运行记录存放在 `$AGENTREC_HOME/runs`；否则存放在 `~/.local/share/agentrec/runs`。运行目录以 `0700` 权限创建，目录内每个文件均以 `0600` 权限创建，`report.md` 也不例外。证据包可能引用私有仓库，因此只有创建它的用户可读。每次运行对应一个目录，其中包含 `manifest.json`、`prompt.txt`、经净化的事件流和 stderr、`actions.jsonl`、`process/result.json`、`git/`（基线、结果和未跟踪文件内容）、`verification/results.json` 以及 `report.md`。只有当提供方在 stdout 输出了非事件内容时，才会有 `provider-stdout.unparsed.log`；若运行只输出事件，就不会留下一个空文件来暗示其他情况。

## 状态与退出码

状态只按记录值展示，绝不推断：

- **仓库**：`AVAILABLE`（已测量）、`UNAVAILABLE`（未产生测量结果）、`PENDING`（运行前已写入，但始终未得到结果）。仅当状态为 `AVAILABLE` 时显示计数：`PENDING` 运行中显示的零表示*未测量*，而不是*测得为空*。
- **验证**：`PASS`、`FAIL`、`TIMEOUT`、`ERROR`、`TAINTED`。未请求验证的运行会显示 `(none)`，这不表示检查已通过。
- **配置污染**：`--verify` 会在提供方启动前固定 `.agentrec.yaml` 及其 SHA-256。若运行重写了该文件，验证将记录为 `TAINTED`，原因为 `config_changed`，**不会执行任何检查**，已固定的检查仍保持 `PENDING`。

退出码：提供方完成且验证（如有）通过时为 `0`；`1`–`125` 表示透传提供方自身的退出码；记录、渲染或验证失败为 `1`；错误调用 agentrec 为 `2`；中断为 `130`。

`--timeout <duration>` 接受 `30s`、`5m`、`2h` 等正数 Go duration，且只限制提供方
进程。到达期限时，agentrec 会向提供方进程组发送 SIGTERM，等待固定的 5 秒终止宽限期；
若进程组仍在运行，再发送 SIGKILL。证据包会以退出原因 `timeout` 完成，agentrec 以 `1`
退出；期限前产生的提供方事件仍会保留。省略该选项时，提供方运行仍不设期限，由 Ctrl-C 或
SIGTERM 控制。仓库测量、验证检查和报告写入各自使用独立限制，不属于这个提供方 timeout
的范围。

无论 Ctrl-C 还是 SIGTERM，agentrec 都会捕获而不是立即在收到信号的位置终止，并且这一行为覆盖整个记录过程，而不只是在提供方运行期间：agentrec 会停止提供方进程组、完成 manifest、测量仓库、执行固定检查、写入报告，最后以 `130` 退出。这样，无论在上述序列的哪一步被中断，运行记录都会说明它如何结束，而不是停在 `PENDING`。第一个信号是最后一个被捕获的信号：之后会恢复交由操作系统处理，因此第二次 Ctrl-C 会在当前位置结束进程。

`process/result.json` 会在进程正常退出时记录退出码，在进程被信号终止时记录终止信号。被信号终止的进程没有退出码，两个字段也不会相互推断。

## 安全

- **持久化前进行结构化脱敏。** 提供方事件、stderr 和非事件 stdout 行都会先脱敏再写入。字段名规范化后，值若位于以 17 个秘密后缀之一结尾的字段下（`TOKEN`、`SECRET`、`PASSWORD`、`APIKEY`、`PASSPHRASE`、`AUTHORIZATION`、`COOKIE`、……），或者匹配 `NAME=VALUE` 赋值及 13 种厂商令牌形态（GitHub、OpenAI、AWS、Google、Stripe、JWT、Slack 令牌与 Webhook、GitLab、npm、Hugging Face、PyPI），都会变成 `[REDACTED:n]`。按后缀而非子串匹配，才能让 `PUBLIC_KEY`、`primaryKey` 和 `token_id` 保持可读。每份 manifest 都会标注规则版本；标记为 `1` 和 `2` 的证据包使用了不同规则判定，因此其脱敏计数不可比较。
- **未跟踪文件内容会被保存**在 `git/untracked/` 下，哈希针对净化后的文本计算。若对原始文本计算哈希，短秘密可能被通过猜测还原。
- **报告绝不嵌入原始提供方事件流、跟踪文件的补丁或未跟踪文件内容。** 报告会包含规范化的提供方派生摘要：每个操作会被简化为标签、一个允许列表中的详情字段和固定摘要字段；控制字符会被转义，因此提供方字符串无法伪造时间线行或操纵终端。读取证据包时会采取防御性措施，符号链接会被拒绝而非跟随，文件大小、行长度和条目数也都受到限制。
- **脱敏计数为零不代表不存在秘密。** 它仅表示没有规则匹配。秘密若位于未命名字段中、出现在普通文本而非赋值中，或长度短于最小限制，结果同样为零。

## 非目标

- **并非系统调用级别的完整观测。** 智能体工作时没有任何机制对其进行观测。agentrec 记录的是提供方报告的内容、运行前后仓库的状态，以及之后独立检查给出的结果。
- **仓库差异不是因果归属。** 变更发生在运行期间，并不等于由智能体造成。任何其他进程对检出的编辑都会落在同一份差异中，每份报告都会注明这一点。同样，通过验证只说明固定检查在运行留下的工作树上通过。
- **不记录交互式会话**，也不提供策略引擎、沙箱或远程上传；agentrec 只在本地观测和写入。

**支持范围：macOS 和 Linux。** Windows 尚未构建或验证；移植不仅需要进程组监管（`internal/runner/process_unix.go`），还需要 verification process control（`internal/evidence/verification.go`）和 repository locking（`internal/lock/repository.go`）。

## 证据

行为相关主张由 [docs/dogfood/2026-07-28-evidence.md](docs/dogfood/2026-07-28-evidence.md) 支持：其中包括一个固定的 20 次尝试检查点及后续的真实变更，覆盖验证 `FAIL`、提供方非零退出、配置 `TAINTED`、中断，以及这些运行**不能**证明的事项。

`agentrec shadow run` 的真实提供方成功路径由 [docs/dogfood/2026-07-29-shadow-evidence.md](docs/dogfood/2026-07-29-shadow-evidence.md) 覆盖：一次 macOS 运行从同一提交出发，分别使用 Claude Code 和 Codex；两组固定验证均通过，两个工作树均已移除，两个证据包均得以保留。该运行未能证明真实提供方的失败和中断情形，也未能证明 Linux 运行时路径；这些生命周期路径由受控仓库测试覆盖。

## 开发

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

`.github/workflows/release.yml` 会在 `v*.*.*` 标签上运行同一脚本，检查每个归档的文件清单和所构建二进制的版本输出，全部通过后才发布。若对应发布版本已存在，工作流会拒绝运行。

## 许可证

agentrec 采用 [MIT 许可证](LICENSE)发布。第三方归属声明和依赖许可证保存在 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) 中。
