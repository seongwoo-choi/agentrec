<div align="center">

# agentrec

**面向编码智能体的飞行记录仪 —— 每一次运行都会留下一份本地的、带归属标注的证据包，终端关闭之后仍然可以读。**

[![CI](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml/badge.svg)](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[English](README.md) | [한국어](README.ko.md) | [日本語](README.ja.md) | 简体中文

</div>

## 问题

编码智能体跑完之后，你手上剩下的只有回滚缓冲。它会被刷走，会把智能体*声称*做过的事
和实际发生的事混在一起，而且对仓库是否还能构建只字不提。

agentrec 把一次非交互式的 Claude Code 或 Codex 运行记录成一个证据包：一条规范化的
动作时间线、受监督进程的结果、运行窗口前后仓库的差异，以及仓库自身固定下来的检查的
结果。它们各自来自不同的观察者，证据包把它们分开保存。

## 四个证据层

| 层 | 观察者 | 含义 | 记录的归属 |
|---|---|---|---|
| **提供方报告的动作** | 智能体 | 智能体声称自己做了什么 —— 工具调用、shell 命令、文件读取与编辑、MCP 调用、Codex 的文件变更。会被规范化和归纳，但绝不当作事实证明。 | `provider_reported` |
| **监督方观察到的结果** | agentrec | 提供方进程是如何结束的：退出码、退出原因、信号、耗时、警告数。 | `supervisor_observed` |
| **仓库观察到的变更** | agentrec | 运行前固定的那个提交与运行后工作树之间的差异，由 agentrec 自己测量。 | `observed during run, not causal proof` |
| **验证观察到的结果** | agentrec | 提供方停止之后，agentrec 运行仓库自身固定的检查时，这些检查是如何结束的。它对工作是怎么做的只字不提。 | `verification_observed` |

只携带提供方进度、协作等待或待办列表生命周期的事件属于流元数据：它们不指向任何动作，
也不会抬高警告数。

根本就不是提供方事件的那一行 stdout —— 更新横幅、弃用警告，以及智能体 CLI 在事件流
旁边打印的任何东西 —— 会被保留在 `provider-stdout.unparsed.log` 中，像其他一切一样
被脱敏，在清单中以 `unparsedLines` 计数，并在报告中点明。它不会被混进事件里，也不会
让这次运行失败：打印了一行散文的提供方同样是运行过的，而丢掉那份记录就是在摧毁
agentrec 存在的意义所在的证据。

## 快速开始

**前置条件。** 从源码构建需要 Go 1.26 或更高版本。受支持的提供方 CLI 必须已经在
`PATH` 上；agentrec 只启动它，绝不安装它。超出支持范围的版本会被拒绝，而不会在
「它的事件流大概还对得上」的假设下被记录。`agentrec trace
--allow-unsupported-version` 会覆盖这次拒绝：运行会被记录，清单上会标注
`versionUnverified`，并且每一份报告都会说明这一点 —— 时间线是由一个并不声称理解该
版本事件流的解析器读出来的，而其余三个证据层完全不依赖解析器。`agentrec shadow run`
没有这个覆盖，因为一条被正确读出的时间线和一条没有被正确读出的时间线之间的比较，
并不是比较。

| 提供方 | 可执行文件 | 支持范围 | 说明 |
|---|---|---|---|
| Claude Code | `claude` | `>=2.1.0, <3.0.0` | 需要 `-p`/`--print`。agentrec 会注入 `--output-format stream-json --verbose --include-hook-events`。 |
| Codex | `codex` | `>=0.144.0, <1.0.0` | `exec` 必须是第一个参数。agentrec 会注入 `--json`。 |

每个打了标签的发布版本包含四个归档 —— `darwin_amd64`、`darwin_arm64`、
`linux_amd64`、`linux_arm64` —— 外加一个覆盖这四者的 `SHA256SUMS` 文件。归档解开后
是一个目录，里面有 `agentrec`、`LICENSE`、`THIRD_PARTY_NOTICES.md` 和
`third_party/licenses/Apache-2.0.txt`。

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

`agentrec version`（等价于 `agentrec --version`）打印三行：版本、构建所用的提交，
以及 UTC 构建时间。发布版二进制带有标签、完整的提交 SHA 和一个 RFC 3339 时间戳；
以其他任何方式做出来的构建报告 `dev`、`unknown` 和 `unknown`，因此未打戳的二进制
绝不会被误认为发布版。

**把验证配置提交进仓库。** 一次运行只会针对仓库已经持有的检查来验证。把
`.agentrec.example.yaml` 复制成 `.agentrec.yaml` 并提交 —— 每条命令都是直接启动的，
不经过 shell，所以参数就是参数，不是别的东西：

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

**记录一次运行。** 工作目录必须是一个 Git 检出，没有未提交的改动，也没有进行中的
操作，这样才能把本次运行自己的改动区分出来。每个仓库同一时间只允许一次运行：第二次
会被拒绝，而不是排队。

```bash
# Claude Code
agentrec trace claude -- -p "add a regression test for the parser"
agentrec trace claude --verify -- -p "add a regression test for the parser"

# Codex
agentrec trace codex --verify -- exec "add a regression test for the parser"

# 针对本解析器并非为之编写的提供方版本进行记录
agentrec trace claude --verify --allow-unsupported-version -- -p "..."

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

`agentrec list` 按从新到旧打印运行记录，`PROJECT` 列取自清单所记录的工作目录的最后
一段；清单里存放的若不是绝对路径，则报告 `unknown`，而不是去猜。

`--cwd` 匹配的是**恰好一个目录**，不是前缀：给定的路径会被转成绝对路径并规范化，
只有当清单自身的工作目录 —— 它本身也是绝对路径，并以同样方式规范化 —— 恰好就是它时，
这次运行才会被保留。子目录是另一个路径，经由符号链接进来的另一条路径也是。

## 报告长什么样

`agentrec show` 是只读的：它从证据包里渲染一次运行，不写入任何东西。下面是一次真实
记录的运行的节选（`582ee874`，裁剪到只剩一个动作）：

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

`agentrec trace` 会在打印任何内容之前，把对同一个证据包的同一份读取写入
`<run>/report.md`，只写一次，绝不再写：已经以那个名字存在的报告会被拒绝，而不是被
覆盖。

## 在一个任务上比较两个智能体

`agentrec shadow run` 把同一个任务记录两次 —— 一次用 Claude Code，一次用 Codex ——
都从同一个已提交的基线出发，并把记录下来的两次运行并排打印：

```bash
agentrec shadow run task.md --runner claude --runner codex
```

每一条支路都在一个一次性的**分离式 Git 工作树**中记录，该工作树从源仓库的 `HEAD`
创建，位于 `$AGENTREC_HOME/shadow/<group>/<runner>`，模式为 `0700`，并在该支路的
证据封存之后被移除。两条支路都会留下普通的运行证据包，因此在检出目录消失之后，
`agentrec list` 和 `agentrec show <run-id>` 仍可把它们读回来。比较本身打印到标准
输出；每条支路持久的 `report.md` 留在各自的证据包里。

比较为每个 runner 打印一个区块，始终按这个顺序，字段也按这个顺序 —— 运行 ID、检查是
怎么结束的以及它固定在什么之上、进程是怎么结束的、这次运行在自己的检出里留下了什么，
以及它做了多少事：

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

`Order` 是每条支路实际运行的次序，而不是这些区块被打印出来的次序：runner 区块始终按
`claude`、`codex` 渲染，好让两位运维读到同一份比较，而恰恰是这个固定次序会掩盖哪个
智能体先跑。

这条命令给你什么，不给你什么：

- **隔离缩小了互相干扰，但它不是因果归属。** 每条支路的仓库增量仍然被记录为
  `observed during run, not causal proof`。
- **没有评分，没有优胜者，没有推荐。** 比较展示的是记录下来的字段，不展示任何由它们
  推导出的东西。该偏向哪次运行，是读者的判断。提供方报告的成本和 token 字段目前不在
  记录的证据里，所以比较不会展示它们。
- **这是 Git 检出，不是字节级 hermetic sandbox。** 未被跟踪的 `.env` 文件和本地凭据
  不会复制到支路中。被跟踪的文件由用户的 Git 检出，因此已配置的 attribute、filter 和
  hook 仍会生效。agentrec 不增加凭据传输，也不执行工作区准备步骤。
- **无法准备的仓库会被拒绝，而不是准备到一半。** 已提交的 `.gitmodules`，或已提交的
  Git LFS 指针文件，会在任何检出存在之前就被拒绝。
- **任务是一个命令行参数。** 任务文件只被读取一次 —— 一个不超过 64 KiB 的、非符号
  链接的普通 UTF-8 文件 —— 然后以 `claude -p -- <task>` 和
  `codex exec --json -- <task>` 的形式交给各个智能体。从标准输入给出的提示词，或者
  拆散到多个参数里的提示词，在这里不受支持。
- **验证是强制的，两条支路是串行的。** 两次运行都针对已提交的 `.agentrec.yaml` 做
  验证，并且一条接着一条执行。检查不会重叠，但可变的认证、缓存、网络服务和其他外部
  状态不会在支路之间重置；输入的 runner 顺序因此可能影响第二个提供方观察到的状态。
  比较会显示每条支路的 `Order` 并说明这一点，因此两个结果绝不会被当作是在完全相同的
  条件下产生的。
- **链接工作树不是安全边界。** 它共享仓库的公共 Git 目录和引用，提供方也可以明确访问
  源检出。锁只协调 agentrec 进程。agentrec 在移除每个 owned worktree 后，把源仓库的
  `HEAD`、状态、索引、引用、工作树列表和公共仓库 config 与预检 snapshot 比较。
  发现 drift 时不会启动下一条支路，并以 `1` 退出；但如果运行也被中断，则 `130` 优先。
  agentrec 会报告 drift，但不会做破坏性恢复。

退出码：用法错误或预检拒绝为 `2` —— 同一个 runner 指定了两次或者根本没指定、任务
文件不可读、检出不干净、`.agentrec.yaml` 未提交、`AGENTREC_HOME` 位于仓库内部 ——
这些都发生在任何检出或提供方存在之前。然后是：两条支路都完成且两次验证都通过为 `0`，
某条支路失败、以未完成状态结束、改变了源仓库，或某个检出无法移除为 `1`；即使同时观察到
drift，只要运行被中断就为 `130`。
**提供方自身的退出码是它那份证据包里的证据，聚合命令绝不会把它透传出来。**

在最终提供方启动决策时已经被保持或排队的中断会阻止该提供方启动。在这个 userspace
决策之后送达的中断会停止当前支路的进程组；POSIX 信号传递和进程启动不是一个 atomic
操作。agentrec 会封存该支路的证据、移除检出，并且绝不启动下一条支路；此时比较会把
没有运行的 runner 显示为 `(not run)`。如果 agentrec 被
直接杀死 —— `SIGKILL`，或者机器宕机 —— 遗留的检出可以通过在源仓库中运行
`git worktree prune` 并删除 `$AGENTREC_HOME/shadow` 下遗留的目录来回收。没有自动的
陈旧工作树清理。

## 运行记录存放在哪里

设置了 `$AGENTREC_HOME` 时在 `$AGENTREC_HOME/runs` 下，否则在
`~/.local/share/agentrec/runs` 下。运行目录以 `0700` 创建，其中的每个文件以 `0600`
创建，`report.md` 也不例外 —— 证据包可能引用一个私有仓库，所以它只对记录它的那个
用户可读。每次运行对应一个目录，里面有 `manifest.json`、`prompt.txt`、经过净化的
事件流和 stderr、`actions.jsonl`、`process/result.json`、`git/`（基线、结果、未跟踪
文件的内容）、`verification/results.json` 和 `report.md`。只有当提供方在 stdout 上
打印了并非事件的东西时，`provider-stdout.unparsed.log` 才会一并出现；只发出事件的
运行不会留下一个假装并非如此的空文件。

## 状态与退出码

状态按记录下来的样子展示，绝不推断：

- **仓库** —— `AVAILABLE`（已测量）、`UNAVAILABLE`（没有产出测量结果）、`PENDING`
  （在运行前写下，始终没有被回答）。计数只在 `AVAILABLE` 时展示：一次 `PENDING`
  运行里的那些 0 意思是*未测量*，不是*测量结果为无*。
- **验证** —— `PASS`、`FAIL`、`TIMEOUT`、`ERROR`、`TAINTED`。没有请求验证的运行显示
  `(none)`，这不是一次通过的检查。
- **配置污染** —— `--verify` 会在提供方启动之前固定 `.agentrec.yaml` 及其 SHA-256。
  如果这次运行重写了那个文件，验证会被记录为 `TAINTED`，原因 `config_changed`，
  **什么都不会被执行**，被固定的那些检查保持 `PENDING`。

退出码：`0` 提供方完成且验证（若有）通过；`1`–`125` 透传提供方自身的退出码；`1`
记录、渲染或验证失败；`2` agentrec 被错误地调用；`130` 被中断。

Ctrl-C 和 SIGTERM 都会被接住而不是就地服从，并且贯穿整个记录过程，而不仅是提供方在
跑的那段时间：agentrec 会停止提供方的进程组、封存清单、测量仓库、运行被固定的检查、
归档报告，然后以 `130` 退出 —— 因此在这个序列的任何一点被中断的运行，都会说明自己是
怎么结束的，而不是停在 `PENDING`。第一个信号是最后一个被接住的信号：此后处理方式交
还给操作系统，所以第二次 Ctrl-C 会让进程就地结束。

`process/result.json` 在进程正常退出时记录退出码，在它被信号杀死时记录终止它的信号。
被信号杀死的进程没有退出码，这两个字段谁也不会从对方推断出来。

## 安全

- **持久化之前做结构化脱敏。** 提供方事件、stderr 以及并非事件的 stdout 行在写入之前
  都会被脱敏。字段名规范化之后以 17 个秘密后缀之一（`TOKEN`、`SECRET`、`PASSWORD`、
  `APIKEY`、`PASSPHRASE`、`AUTHORIZATION`、`COOKIE`、……）结尾的那些值，加上
  `NAME=VALUE` 形式的赋值和 13 种厂商令牌形状（GitHub、OpenAI、AWS、Google、Stripe、
  JWT、Slack 令牌与 Webhook、GitLab、npm、Hugging Face、PyPI），都会变成
  `[REDACTED:n]`。按后缀而不是按子串匹配，正是 `PUBLIC_KEY`、`primaryKey` 和
  `token_id` 得以保持可读的原因。规则版本按清单逐份标注 —— 标注为 `1` 和 `2` 的证据
  包是由不同规则判定的，它们的脱敏计数不可比较。
- **未跟踪文件的内容会被保存**，位于 `git/untracked/` 下，哈希是对净化后的文本计算的
  —— 对原始文本做哈希会让一个短秘密通过猜测被还原出来。
- **报告绝不嵌入原始的提供方事件流、被跟踪文件的补丁或未跟踪文件的内容。** 它们确实
  携带由提供方派生的规范化摘要：一个动作被压缩成一个标签、一个位于允许列表中的细节
  字段，以及若干固定的摘要字段，并且控制字符会被转义，所以任何提供方字符串都无法伪造
  一行时间线，也无法驱动终端。证据包会被防御性地读回，符号链接会被拒绝而不是跟随，
  大小、行长和条目数都有上界。
- **脱敏计数为零不是「不存在秘密」的断言。** 它的意思是没有规则匹配上 —— 一个位于
  无名字段中的秘密、一个出现在散文而非赋值中的秘密，或者一个短于最小长度的秘密，
  产生的同样是零。

## 非目标

- **不是系统调用级完备的。** 在智能体工作期间，没有任何东西在观察它。agentrec 记录的
  是提供方报告了什么、运行前后仓库是什么样子，以及独立的检查在之后说了什么。
- **仓库增量不是因果归属。** 这些变更发生在运行期间；这和「是智能体做的」不是一回事。
  任何别的东西编辑了这个检出，都会落进同一份增量里，每份报告都这么写着。同理，一次
  通过的验证也只说明被固定的检查在这次运行留下的那棵树上通过了。
- **不记录交互式会话**，并且没有策略引擎、没有沙箱、没有远程上传 —— agentrec 只做
  观察，并写在本地。

**支持范围：macOS 和 Linux。** 进程组监督只为 `darwin || linux` 而写
（`internal/runner/process_unix.go`），因此 Windows 未构建也未验证。

## 证据

关于行为的主张由
[docs/dogfood/2026-07-28-evidence.md](docs/dogfood/2026-07-28-evidence.md) 支撑 ——
一个固定的 20 次尝试检查点，加上其后的真实变更，覆盖了验证 `FAIL`、提供方非零退出、
配置 `TAINTED`、中断，以及那些运行**没有**确立的东西。

`agentrec shadow run` 使用真实提供方的成功路径由
[docs/dogfood/2026-07-29-shadow-evidence.md](docs/dogfood/2026-07-29-shadow-evidence.md)
支撑：在 macOS 上从同一个提交分别运行了一次 Claude Code 和 Codex，两组固定验证都通过，
两个 worktree 都被删除，两个证据包都得到保留。这次运行没有确立真实提供方的失败、中断
路径或 Linux 运行时行为；这些生命周期路径由使用受控替身的仓库测试覆盖。

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

`.github/workflows/release.yml` 在 `v*.*.*` 标签上运行同一个脚本，检查每个归档的
内容清单以及它所构建的二进制的版本输出，只有这些都通过才发布。它拒绝对一个已经存在的
发布版本运行。

## 许可证

agentrec 以 [MIT 许可证](LICENSE)提供。第三方署名和依赖许可证保存在
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) 中。
