<p align="center">
  <a href="assets/agentrec-wordmark.svg"><img src="assets/agentrec-wordmark.svg" alt="agentrec — コーディングエージェントのフライトレコーダー" width="100%"></a>
</p>

<table align="center">
  <tr>
    <td width="50%" align="center">
      <a href="assets/viewer-en-light.png"><img src="assets/viewer-en-light.png" alt="agentrec ビューアー: 記録されたセッションを、ツール呼び出し付きの会話、6 つの証拠タイル、証拠インスペクターとともに読み返す様子"></a><br>
      <sub><b>1 回の実行を、読み返す。</b><br>エージェントが言ったこと、プロセスがしたこと、リポジトリが示すこと、チェックが返したことを、混ぜずに並べます。</sub>
    </td>
    <td width="50%" align="center">
      <a href="assets/agentrec-evidence-layers.svg"><img src="assets/agentrec-evidence-layers.svg" alt="agentrec バンドルの 4 つの証拠レイヤー"></a><br>
      <sub><b>4 つの観測者、4 つの帰属。</b><br>何もスコアに合算せず、得られなかった証拠を合格として扱うこともありません。</sub>
    </td>
  </tr>
</table>

# agentrec

<div align="center">

[English](README.md) | [한국어](README.ko.md) | 日本語 | [简体中文](README.zh-CN.md)

[![CI](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml/badge.svg)](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/seongwoo-choi/agentrec?logo=github)](https://github.com/seongwoo-choi/agentrec/releases)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![GitHub stars](https://img.shields.io/github/stars/seongwoo-choi/agentrec?style=flat&logo=github)](https://github.com/seongwoo-choi/agentrec)

</div>

<p align="center">
  <strong>コーディングエージェントの実行ごとに、ターミナルが消えた後も読み返せる、帰属情報付きのローカル証拠バンドルが残ります。</strong><br>
  <em>agentrec が起動した実行でも、対話セッションから記録した実行でも同じです。プロバイダーの主張、プロセス結果、リポジトリ差分、固定されたチェック — それぞれ別の観測者に由来し、決して 1 つのスコアには合算されません。</em>
</p>

**agentrec** は、Claude Code または Codex の 1 回の実行をバンドルとして記録します。
正規化されたアクションのタイムライン、監督対象プロセスの結果、実行ウィンドウを
またいだリポジトリの差分、そしてリポジトリ自身が固定したチェックの結果です。
それぞれ異なる観測者に由来し、バンドルはそれらを分けたまま保持します。だからこそ、
コードレビュー、障害調査、引き継ぎ、新しいエージェントバージョンを信頼するかの判断を、
要約ではなく観測された事実から始められます。

[リリースノート](docs/releases/v0.15.2.md) ·
[設計ノート](docs/plans/2026-07-27-agentrec-flight-recorder.md) ·
[Shadow runner の設計](docs/plans/2026-07-29-shadow-runner.md) ·
[Dogfood の証拠](docs/dogfood/2026-07-28-evidence.md) ·
[サードパーティ通知](THIRD_PARTY_NOTICES.md)

> [!NOTE]
> agentrec は、エージェントをリアルタイムで操作する frontend でも、クラウドテレメトリ
> サービスでも、観測されたすべてのファイル変更をエージェントが引き起こしたことの証明
> でもありません。1 回の実行を囲むローカルな証拠の境界です。何が、誰によって観測され、
> 何を確定できないかを明言するからこそ役に立ちます。

## クイックスタート

**インストール。** Homebrew が最も簡単です。チェックサム付きアーカイブや `go install` でも構いません。

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

各リリースには `darwin_amd64`、`darwin_arm64`、`linux_amd64`、`linux_arm64` のアーカイブと、
それらすべてを収めた `SHA256SUMS` が 1 つ付属します。`agentrec version` はタグ・コミット・UTC
ビルド時刻を表示し、それ以外の方法で作ったビルドは `dev` と答えます。ソースからのビルドには
Go 1.26 以降、`shadow run` には Git 2.36 以降が必要です。複数のインストールがあり得る場合は
`agentrec version --verbose` が実際に実行されたファイルと `PATH` 上のすべての `agentrec` を
示します。

**実行の検証に使うチェックを固定する** には `.agentrec.yaml` をコミットします
(`.agentrec.example.yaml` をコピー)。各コマンドはシェルを介さず直接起動されます。

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

**agentrec が起動する実行を記録する。** 作業ディレクトリはクリーンな Git チェックアウトである
必要があり、リポジトリごとに同時に記録できるのは 1 件です。

```sh
agentrec trace claude -- -p "add a regression test for the parser"
agentrec trace claude --verify -- -p "add a regression test for the parser"
agentrec trace claude --timeout 30m -- -p "add a regression test for the parser"
agentrec trace codex --verify -- exec "add a regression test for the parser"
agentrec trace claude --verify --allow-unsupported-version -- -p "..."
```

**すでに使っている対話型セッションを記録する。** `setup` がプロバイダーのフックを
インストールします(ユーザーファイルまたはプロジェクトファイル、既存フックは保持、隣に
バックアップを作成、再実行しても変化なし)。以後に開くすべてのセッションが実行として
記録されます。Codex は新しいフックを信頼するため、Codex 内で一度 `/hooks` を実行する必要が
あります。

```sh
agentrec setup
agentrec setup --claude --verify
agentrec setup --codex --project
agentrec hooks print --claude
```

**読み返す。** `start` はビューアーを `http://127.0.0.1:7788/` でバックグラウンド起動し、
`view` はフォアグラウンドで起動し、`list`・`show`・`events` は同じバンドルをターミナルで
読みます。

```sh
agentrec start
agentrec status
agentrec stop
agentrec view latest
agentrec list
agentrec show latest
agentrec events latest --json
```

## ビューアー

<table align="center">
  <tr>
    <td width="50%" align="center">
      <a href="assets/viewer-en-dark.png"><img src="assets/viewer-en-dark.png" alt="ダークモードの agentrec ビューアー"></a><br>
      <sub><b><code>agentrec view</code>。</b> 読み取り専用、ループバック専用、外部アセットなし。</sub>
    </td>
    <td width="50%" align="center">
      <a href="assets/agentrec-evidence-layers.svg"><img src="assets/agentrec-evidence-layers.svg" alt="4 つの証拠レイヤー"></a><br>
      <sub><b>同じバンドル、4 つのレイヤー。</b> どの要約も、変わらない記録の上の折りたたみ・ラベル・位置にすぎません。</sub>
    </td>
  </tr>
</table>

- **run 一覧** — タイトルを先頭にした行に、プロバイダー・プロジェクト・時刻・所要時間と、
  別々に示されるプロセス判定と検証判定。検索、プロジェクト選択、折りたたまれた詳細フィルター、
  読み込んだ run をプロバイダー・検証結果・プロジェクトごとに数える折りたたみの集計。
- **run 詳細** — リクエストとエージェントの最後のメッセージを並べて表示。プロセス・検証・
  リポジトリ・警告の 4 枚のサマリー。失敗した run の failure triage。**Verify now**、または
  `--allow-run` なしで起動した場合は後で検証するためのコマンド。
- **タイムライン** — **読み取りビュー** は定型的なツールアクションを折りたたみ、プロンプト
  (`自分 · 3 / 12`)、返答、編集、失敗、不明な状態はそのまま表示します。**変更** はファイルを
  ディレクトリごとにまとめ、**プロバイダーイベント** は `PostToolUse` とフックのライフサイクル
  記録を折りたたみます。**全アクション/全ファイル/全イベント** はトグル 1 つで、どの行も
  インスペクターで元の記録を開けます。
- **run を横断して** — すべての run から単語を検索し、該当するアクションや変更ファイルに直接
  到達。任意の 2 つの run を並べて比較。正確な行へのローカル証拠リンクをコピー。
- **ライブ** — 進行中の run はページが自動で更新され、現在の作業ツリーを表示します。

## 4 つの証拠レイヤー

| レイヤー | 観測者 | 意味 | 記録される帰属 |
| --- | --- | --- | --- |
| 🗣️ **プロバイダー報告のアクション** | エージェント | エージェントが「やった」と言ったこと — ツール呼び出し、シェルコマンド、ファイルの読み取りと編集、MCP 呼び出し、Codex のファイル変更。正規化し要約しますが、証明とはみなしません。 | `provider_reported` |
| 👁️ **スーパーバイザー観測の結果** | agentrec | プロバイダープロセスがどう終わったか: 終了コード、終了理由、シグナル、所要時間、警告数。agentrec が起動していないセッションは `NOT OBSERVED`。 | `supervisor_observed` |
| 🌳 **リポジトリ観測の変更** | agentrec | 実行前に固定したコミットと実行後の作業ツリーの差分。agentrec 自身が計測します。 | `observed during run, not causal proof` |
| ✅ **検証観測の結果** | agentrec | プロバイダー停止後に agentrec がリポジトリ自身の固定チェックを実行した結果。作業がどう行われたかについては何も語りません。 | `verification_observed` |

## 2 つの記録方法

| | 🚀 `agentrec trace` | 🎧 対話型セッション |
| --- | --- | --- |
| プロバイダーを起動するのは | agentrec(親プロセスとして) | いつも通りあなた。プロバイダーのフックが agentrec に報告 |
| スーパーバイザー観測の結果 | 終了コード、シグナル、所要時間 | `NOT OBSERVED`。`Ended By` が `SessionEnd` フックが終了を報告したか、レコーダーが諦めたか(`session_lost`、フックなしで 8 時間)を示す |
| ベースライン | プロセス開始前に固定 | `SessionStart` フック到着時に固定 |
| チェックアウトの状態 | クリーンであること。リポジトリごとに run 1 件 | ダーティなチェックアウトや同時セッションも拒否せず記録 |
| 検証 | `--verify` が起動前に `.agentrec.yaml` を固定 | `--verify` 付きで出力したフラグメントのみ。かつ `.agentrec.yaml` が追跡済みで `HEAD` と同一の場合のみ |

Codex は `PostToolUseFailure` を送らないため、失敗したコマンドは応答に失敗と書かれた完了
アクションとして現れ、`apply_patch` の編集はパッチヘッダーにファイル名を記します。
セッションが無効化したフックは空白を残すだけで、なかったことにはなりません。

## コマンド

| コマンド | 動作 |
| --- | --- |
| 🚀 `agentrec trace <claude\|codex> [--verify] [--allow-unsupported-version] [--timeout <d>] -- <args...>` | agentrec が起動・監督する非対話型の実行を 1 件記録します。 |
| 🧩 `agentrec setup [--claude] [--codex] [--verify] [--project] [--uninstall]` | 対話型セッションを記録するフックをインストールします。フラグがなければ質問します。 |
| ▶️ `agentrec start [--listen <loopback-address>] [--no-open] [--allow-run]` | ビューアーをバックグラウンドで起動します。`--allow-run` ならページから比較と後からの検証を開始できます。 |
| ⏹️ `agentrec stop` · ℹ️ `agentrec status` | バックグラウンドのビューアーを停止 · ビューアー、run 数、フックのインストール状況を報告。 |
| 🖥️ `agentrec view [<run-id>\|latest] [--listen <loopback-address>] [--no-open] [--allow-run]` | 読み取り専用ビューアーをフォアグラウンドで起動します。 |
| 📋 `agentrec list [--cwd <path>] [--exit-reason <reason>] [--verification-status <status>] [--failures-only] [--json]` | run を新しい順に一覧します。`--json` はスキーマバージョン付きです。 |
| 📄 `agentrec show <run-id>\|latest [--failures-only] [--json]` | バンドルから run を 1 件描画します。何も書き込みません。 |
| 🗂️ `agentrec changes <run-id>\|latest [--json]` | パッチやファイル内容なしで変更ファイル一覧(最大 250 件)を表示します。 |
| 🧾 `agentrec events <run-id>\|latest [--json]` | 記録されたプロバイダーイベントを要約またはダンプします。 |
| ✅ `agentrec verify <run-id>\|latest` | コミット済みのチェックを現在のリポジトリに対して今実行し、結果を後からの計測として run の隣に記録します。 |
| 🗑️ `agentrec trash [restore <run-id> \| empty \| sweep <age>]` | ビューアーから削除した run を一覧・復元・消去・掃除します。 |
| 🎧 `agentrec hooks print --claude\|--codex [--verify]` | `setup` がインストールするフックのフラグメントを出力します。 |
| ⚖️ `agentrec shadow run <task-file> --runner claude --runner codex` · `shadow show <group-id>` | 1 つのタスクを同じコミットのベースラインから隔離された worktree で 2 回記録 · 比較を再描画。 |
| 🏷️ `agentrec version [--verbose]` | タグ・コミット・UTC ビルド時刻を表示します。`--verbose` は `PATH` 上のすべての `agentrec` を一覧します。 |

すべてのコマンドが `-h`/`--help` を受け付けます。`agentrec hook <provider>` と
`agentrec session serve` も存在しますが、前者はプロバイダーが、後者は最初のフックが実行します。

## レポートはこう見える

`agentrec show` はバンドルから run を描画し、何も書き込みません。実際の run からアクション
1 件だけを残した抜粋:

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

`agentrec trace` は同じ内容を `<run>/report.md` に一度だけ書きます。その名前にすでに
レポートがあれば、上書きせず拒否します。

## 2 つのエージェントを 1 つのタスクで比較する

```sh
agentrec shadow run task.md --runner claude --runner codex
agentrec shadow show <group-id>
```

1 つのタスクを Claude Code で 1 回、Codex で 1 回、同じコミットのベースラインから
`$AGENTREC_HOME/shadow/<group>/` 配下の使い捨ての分離 worktree にそれぞれ記録します。
どちらの脚も通常の run バンドルを残します。

| 得られるもの | 得られないもの |
| --- | --- |
| 同じコミットからの 2 つの run。同じコミット済み `.agentrec.yaml` で検証 | スコア、勝者、推奨 — 判断は読む人に委ねられます |
| 脚どうしの干渉を抑える分離。ある脚の後にソースが変わると次の脚を止める | 因果の帰属 — 各差分は依然として `observed during run, not causal proof` |
| 何も作られる前の拒否は終了 `2`、脚は `0`/`1`、中断は `130` | サンドボックス — リンクされた worktree は共通の Git ディレクトリを共有し、追跡外の `.env` はコピーされません |

## 主張より証拠

状態は記録されたまま表示し、推測しません:

| 表示 | 意味 |
| --- | --- |
| `AVAILABLE` | リポジトリを計測しました。件数はここでのみ表示されます。 |
| `NOT RUN` | 検証が要求されませんでした。中立であり、合格ではありません。 |
| `NOT OBSERVED` | 監督したプロセスがありません: agentrec が起動していないセッション。 |
| `NOT RECORDED` | リポジトリを計測しませんでした。中立であり、合格ではありません。 |
| `PENDING` | 実行前に書かれ、回答がありません。0 は*未計測*を意味します。 |
| `PASS` / `FAIL` / `TIMEOUT` / `ERROR` | run が残したツリー上で固定チェックがどう終わったか。 |
| `TAINTED` | run が固定後に `.agentrec.yaml` を書き換えました: **何も実行されていません**。 |
| `completed` / `nonzero` / `timeout` / `interrupted` | agentrec が見た監督プロセスの終わり方。 |
| `session_ended` / `session_lost` / `running` / `unknown` | `SessionEnd` フックが終了を報告した — またはレコーダーが待つのをやめた、まだ待っている、どう終わったか書けずに終わった。 |

| 終了コード | 意味 |
| --- | --- |
| `0` | プロバイダーが完了し、検証があれば合格しました。 |
| `1`–`125` | `trace` がそのまま渡したプロバイダー自身の終了コード。 |
| `1` | 記録、描画、または検証の失敗。 |
| `2` | agentrec の呼び出し方が誤っています。 |
| `130` | 中断 — プロバイダーグループを止め、リポジトリを計測し、チェックを実行し、レポートを書いた後です。 |

agentrec が主張しないこと:

- **syscall レベルで完全ではありません。** 記録はプロバイダーが報告したこと、実行前後の
  リポジトリの状態、そしてその後に独立したチェックが語ったことです。
- **リポジトリの差分は因果の帰属ではありません。** チェックアウトを編集した他の何かも同じ
  差分に入り、すべてのレポートがそう述べます。
- **セッションの終わりはプロバイダーの言葉です。** 誰が run を終えたかはレポートが述べます。
- **ポリシーエンジンも、サンドボックスも、リモートアップロードもありません。** macOS と
  Linux をサポートし、Windows はビルドも検証もされていません。

## セキュリティ

- **ビューアーはブラウザではなくマシンを信頼します。** 認証なしでループバックを待ち受ける
  ため、ローカルのプロセスはすべての run を読み、ゴミ箱へ移せます。他オリジンのページには
  できません: 削除にはビューアー自身のページだけが読めるトークンが必要です。消去するのは
  `agentrec trash empty` だけです。`--allow-run` ではローカルプロセスがあなたとして
  `agentrec shadow run` も起動できるため、望まないならフラグは付けないでください。
- **永続化前の構造的な秘匿化。** 17 種の秘密フィールド接尾辞(`TOKEN`、`SECRET`、
  `PASSWORD`、`APIKEY`、`COOKIE`、…)の値、`NAME=VALUE` 代入、13 種のベンダートークン
  形式が `[REDACTED:n]` になります。秘匿化 0 件は秘密がないという主張ではありません。
- **レポートは生のイベントストリーム、追跡パッチ、追跡外ファイルの本文を埋め込みません。**
  アクションはラベルと許可リストのフィールドに縮約され制御文字はエスケープされ、バンドルは
  防御的に読み込まれます(シンボリックリンク拒否、サイズ制限)。
- **リポジトリ証拠は Git の既定値に固定** され、リポジトリ属性や運用者の設定がパッチを
  書き換えることはできません。
- **リリースアーカイブはチェックサム付きですが署名はありません。** `SHA256SUMS` は
  成果物の同一性を示すもので、公開者の身元を示すものではありません。

## run の保存場所

`$AGENTREC_HOME/runs`、なければ `~/.local/share/agentrec/runs`。ディレクトリは `0700`、
ファイルは `0600`。run ごとに 1 ディレクトリに `manifest.json`、`prompt.txt`、秘匿化済みの
イベントストリームと stderr、`actions.jsonl`、`process/result.json`(trace 実行)、`git/`、
`verification/results.json`、`report.md` が置かれます。削除した run は `trash/` で待ちます。
`AGENTREC_HOME` は記録対象のリポジトリの外になければなりません。

## ドキュメント

- [リリースノート](docs/releases/) — リリースごとに 1 ファイル。最新は [v0.15.2](docs/releases/v0.15.2.md)
- [Flight recorder の設計](docs/plans/2026-07-27-agentrec-flight-recorder.md) · [Shadow runner の設計](docs/plans/2026-07-29-shadow-runner.md)
- [Dogfood の証拠 — レコーダー](docs/dogfood/2026-07-28-evidence.md) · [shadow run](docs/dogfood/2026-07-29-shadow-evidence.md)
- [ビューアー設計契約](DESIGN.md) · [サードパーティ通知](THIRD_PARTY_NOTICES.md)

## 開発

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

`scripts/build-release.sh` はアーカイブをローカルで作るだけで、何も公開しません。
`release.yml` は `v*.*.*` タグで同じスクリプトを実行し、すべてのアーカイブを点検してから
公開します。既存のリリースは上書きしません。Homebrew tap はリリースごとに実際の
`brew install` と `brew test` で検証してから formula を更新します。

## 翻訳の維持

`README.md` が正本です。ローカライズ版 README は逐語訳ではなくその言語の読者のために
書きますが、すべてのコマンド・リンク・サポートバージョン範囲・安全上の注意はそのまま
保ちます。チェッカーは自動化で証明できることだけを見ます: 見出し構造、実行可能な
コードブロック、外部リンク。

```sh
python3 scripts/check-readme-localizations.py
sh scripts/check-readme-localizations_test.sh
```

## ライセンス

agentrec は [MIT ライセンス](LICENSE) で提供されます。サードパーティの帰属表示と依存関係の
ライセンスは [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) に保存されています。
