<div align="center">

# agentrec

**コーディングエージェントのフライトレコーダー。ターミナルが消えた後も読み返せる、帰属情報付きのローカル証拠バンドルを、実行ごとに残します。**

[![CI](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml/badge.svg)](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[English](README.md) | [한국어](README.ko.md) | 日本語 | [简体中文](README.zh-CN.md)

</div>

## 課題

コーディングエージェントの作業が終わったあとに残るのは、たいていターミナルの
スクロールバックだけです。これはやがて失われ、エージェントが「行った」と報告した
内容と実際に起きたことを混同しがちです。さらに、リポジトリが今もビルドできるかは
何も分かりません。

agentrec は、非対話モードで実行した Claude Code または Codex の 1 回の実行を、
バンドルとして記録します。バンドルには、正規化されたアクションのタイムライン、
監督対象プロセスの結果、実行前後のリポジトリ差分、そしてリポジトリ自身に固定された
チェックの結果が含まれます。それぞれ異なる観測者に由来する情報であり、バンドル内でも
区別したまま保持されます。

## agentrec を使う理由

エージェント実行を一時的なターミナルセッションで終わらせず、コードレビュー、
障害調査、引き継ぎ、新しいエージェントやプロバイダーバージョンを信頼するかの判断に
役立つ記録として残したいときに、agentrec を使います。

- **要約を鵜呑みにせずレビューする。** `report.md` は、プロバイダー報告のアクション、プロセス結果、測定済みのリポジトリ差分、実際に実行されたチェックを分けて示します。レビュー担当者は、主張・変更・検証をそれぞれ独立して確認できます。
- **失敗または不審な実行を後から調査する。** バンドルには終了理由、stderr の文脈、警告、解釈できなかった provider stdout、実行中に観測したリポジトリ状態が残ります。スクロールバックが失われた後でも、timeout、parser mismatch、non-zero exit、予期しない diff を調べられます。
- **再現可能な引き継ぎを行う。** 開始コミットと verification config を固定し、チェックの結果を記録します。次のエンジニアは、誰かの記憶ではなく、永続する artifact と command を受け取れます。
- **勝者をでっち上げずにエージェントを比較する。** `shadow run` は、単一の baseline から Claude と Codex に別々の worktree と evidence bundle を与えます。記録された事実を提示するだけで、action 数・diff・チェック結果を根拠のない score には変換しません。
- **プロバイダーのアップグレードを慎重に扱う。** サポート対象外の provider version はデフォルトで拒否します。明示的な override を使うと manifest と report に `versionUnverified` が残るため、parser リスクを抱える timeline を、完全に理解された証拠と後から誤認することを防げます。

agentrec は、対話型の実行記録 UI やクラウドテレメトリサービスではありません。また、
エージェントが観測されたすべてのファイル変更を引き起こしたことの証明でもありません。1 回の
非対話的な実行を対象に、誰が何を観測したかと、何を確定できないかを残すためのローカルな
証拠の境界です。

## 翻訳版の保守

`README.md` は事実関係の基準となる文書です。各言語の README は逐語訳ではなく、その言語の
読者に自然な技術文書として書きます。ただし、コマンド、リンク、サポート対象バージョンの範囲、
帰属と安全性に関する注意事項は必ず維持します。

自然な文章かどうかは、その言語を読める人がレビューする必要があります。
`scripts/check-readme-localizations.py` が検査するのは、自動化で証明できる契約だけです。
見出しの構造、実行可能なコードブロックの内容、外部リンク先は確認できますが、意味が保たれて
いることまでは保証しません。

```bash
python3 scripts/check-readme-localizations.py
sh scripts/check-readme-localizations_test.sh
```

## 4 つの証拠レイヤー

| レイヤー | 観測者 | 意味するもの | 記録される帰属 |
|---|---|---|---|
| **プロバイダー報告アクション** | エージェント | エージェントが行ったと報告した内容。ツール呼び出し、シェルコマンド、ファイルの読み取り・編集、MCP 呼び出し、Codex のファイル変更など。正規化して要約しますが、証明としては扱いません。 | `provider_reported` |
| **スーパーバイザー観測結果** | agentrec | プロバイダープロセスの終了状況。終了コード、終了理由、シグナル、所要時間、警告数。 | `supervisor_observed` |
| **リポジトリ観測変更** | agentrec | 実行前に固定したコミットと、実行後のワークツリーとの差分を agentrec 自身が測定したもの。 | `observed during run, not causal proof` |
| **検証観測結果** | agentrec | プロバイダー停止後に、agentrec がリポジトリに固定されたチェックを実行した結果。作業がどのように行われたかは示しません。 | `verification_observed` |

プロバイダーの進捗、共同作業の待機、TODO リストのライフサイクルだけを運ぶイベントは、
ストリームのメタデータです。アクションを表すものではなく、警告数にも加算されません。

プロバイダーイベントではない stdout の行、たとえば更新バナー、非推奨警告、エージェント
CLI がイベントストリームと並行して出力するものは、`provider-stdout.unparsed.log` に
保存されます。これらも他の内容と同様に秘匿され、manifest では `unparsedLines` として
数えられ、report に明記されます。イベントに混ぜ込むことも、実行失敗として扱うことも
ありません。散文を 1 行出力したプロバイダーも実際に実行されており、その記録を捨てる
ことは、agentrec が保全すべき証拠を壊すことになるためです。

## クイックスタート

**前提条件。** ソースからビルドするには Go 1.26 以降が必要です。サポート対象の
プロバイダー CLI が `PATH` 上にあらかじめ存在している必要があります。agentrec は
起動するだけで、インストールは行いません。サポート対象外のバージョンは、イベント
ストリームがなお適合すると仮定して記録することはせず、拒否します。
`agentrec trace --allow-unsupported-version` はこの拒否を上書きします。実行は記録され、
manifest には `versionUnverified` が刻まれ、すべての report にその旨が記載されます。
この timeline は、そのバージョンのストリームを理解すると主張しない parser が読んだ
ものです。一方、残る 3 つの証拠レイヤーは parser にまったく依存しません。
`agentrec shadow run` にこの上書きはありません。正しく読める timeline とそうでない
ものの比較は、比較として成立しないためです。

| プロバイダー | 実行ファイル | サポート範囲 | 備考 |
|---|---|---|---|
| Claude Code | `claude` | `>=2.1.0, <3.0.0` | `-p`/`--print` が必要です。agentrec は `--output-format stream-json --verbose --include-hook-events` を注入します。 |
| Codex | `codex` | `>=0.144.0, <1.0.0` | `exec` は最初の引数でなければなりません。agentrec は `--json` を注入します。 |

各タグ付きリリースには、`darwin_amd64`、`darwin_arm64`、`linux_amd64`、
`linux_arm64` の 4 つのアーカイブと、そのすべてを対象にした `SHA256SUMS` ファイルが
含まれます。アーカイブを展開すると、`agentrec`、`LICENSE`、
`THIRD_PARTY_NOTICES.md`、`third_party/licenses/Apache-2.0.txt` を収めた
ディレクトリが 1 つ作成されます。

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

`agentrec version`（または同等の `agentrec --version`）は、バージョン、ビルド元の
コミット、UTC のビルド時刻の 3 行を出力します。リリースバイナリにはタグ、完全な
コミット SHA、RFC 3339 のタイムスタンプがあります。それ以外の方法で作成したビルドは
`dev`、`unknown`、`unknown` を報告します。そのため、スタンプのないバイナリを
リリース済みのものと取り違えることはありません。

**検証設定をコミットする。** 実行を検証できるのは、リポジトリがすでに持っていた
チェックに対してだけです。`.agentrec.example.yaml` を `.agentrec.yaml` にコピーして
コミットします。各コマンドはシェルを介さず直接起動されるため、引数はあくまで引数です:

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

**実行を記録する。** 作業ディレクトリは、コミットされていない変更がなく、進行中の
操作もない Git チェックアウトでなければなりません。これにより、その実行自身による
変更を区別できます。リポジトリごとに同時に実行できるのは 1 件だけです。2 件目は
キューに入らず、拒否されます。

```bash
# Claude Code
agentrec trace claude -- -p "add a regression test for the parser"
agentrec trace claude --verify -- -p "add a regression test for the parser"

# Codex
agentrec trace codex --verify -- exec "add a regression test for the parser"

# このパーサーが対象としていないプロバイダーバージョンに対して記録する
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

`agentrec list` は実行を新しい順に出力します。`PROJECT` 列は manifest に記録された
作業ディレクトリの末尾要素から取得します。絶対パス以外を保持する manifest は、推測せず
`unknown` を報告します。

`--cwd` は前方一致ではなく、**完全に同じ 1 つのディレクトリ**だけに一致します。
指定したパスは絶対パス化・正規化され、manifest 自身の作業ディレクトリも絶対パス化・
同じ方法で正規化されます。実行が残るのは両者が完全に一致するときだけです。
サブディレクトリは別のパスであり、シンボリックリンク経由の別経路も同様です。

## レポートの見え方

`agentrec show` は読み取り専用です。バンドル内の実行 1 件を表示するだけで、何も
書き込みません。以下は、実際に記録された実行（`582ee874`、アクションを 1 件に
省略）の抜粋です:

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

`agentrec trace` は出力を行う前に、同じバンドルから読み取った同一の内容を
`<run>/report.md` に書き込みます。これは 1 回だけ行われ、再実行はされません。その名前の
report がすでに存在する場合は、上書きせず拒否します。

## 1 つのタスクで 2 つのエージェントを比較する

`agentrec shadow run` は、単一のコミット済み baseline から 1 つのタスクを 2 回記録します。
1 回は Claude Code、もう 1 回は Codex で実行し、記録された 2 つの実行を並べて出力します:

```bash
agentrec shadow run task.md --runner claude --runner codex
```

各レグは、ソースリポジトリの `HEAD` から作成する使い捨ての**デタッチド Git
ワークツリー**内で記録されます。場所は `$AGENTREC_HOME/shadow/<group>/<runner>`、
モードは `0700` で、そのレグの証拠を確定した時点で削除されます。両方のレグは通常の
実行バンドルを残すため、チェックアウトが消えた後も `agentrec list` と
`agentrec show <run-id>` で読み返せます。比較そのものは標準出力に表示され、各レグの
永続的な `report.md` はそれぞれのバンドルに残ります。

比較はランナーごとに 1 ブロックを、常にこの順序およびフィールド順で出力します。実行 ID、
チェックの終了状況と固定内容、プロセスの終了状況、実行が自身のチェックアウトに残した
内容、そして実行量です:

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

`Order` は各レグの実行順であり、ブロックの表示順ではありません。ランナーブロックは、
2 人の運用者が同じ比較を同じように読めるよう、常に `claude`、`codex` の順で表示されます。
ただしこの固定表示順では実際の実行順が隠れるため、各レグの `Order` を明示します。

このコマンドが提供するものと、提供しないもの:

- **隔離は干渉を限定するが、因果的な帰属ではない。** 各レグのリポジトリ差分は、引き続き `observed during run, not causal proof` として記録されます。
- **スコア、勝者、推奨はない。** 比較は記録済みフィールドを示すだけで、そこから導出した判断は示しません。どちらの実行を選ぶかは読み手が判断します。プロバイダーが報告するコストやトークンのフィールドは、現時点の記録済み証拠には含まれないため、比較にも表示されません。
- **バイト単位で hermetic な sandbox ではなく、Git のチェックアウトである。** 追跡されていない `.env` ファイルやローカル資格情報はレグへコピーされません。追跡済みファイルは利用者の Git によってチェックアウトされるため、設定済みの attribute、filter、hook はそのまま適用されます。agentrec は資格情報の転送やワークスペース準備を追加しません。
- **準備できないリポジトリは、半端に準備せず拒否する。** コミット済みの `.gitmodules`、またはコミット済み Git LFS のポインタファイルがある場合は、チェックアウトを 1 つでも作成する前に拒否します。
- **タスクは 1 つのコマンドライン引数である。** タスクファイルは一度だけ読みます。対象は最大 64 KiB の、シンボリックリンクではない通常の UTF-8 ファイル 1 つです。各エージェントには `claude -p -- <task>` および `codex exec --json -- <task>` として渡します。標準入力で渡すプロンプトや、複数引数に分けたプロンプトはサポートしません。
- **検証は必須で、レグは直列実行される。** 両方の実行をコミット済み `.agentrec.yaml` に対して検証し、片方ずつ順に実行します。チェックは重なりませんが、可変な認証、キャッシュ、ネットワークサービスなどの外部状態はレグ間で初期化されません。そのため、指定したランナー順が 2 番目のプロバイダーの観測に影響し得ます。比較には各レグの `Order` が示されるため、2 つの結果を同一条件で得たもののように解釈することはできません。
- **リンクされたワークツリーはセキュリティ境界ではない。** 共通の Git ディレクトリと参照を共有しており、プロバイダーはソースチェックアウトへ明示的に到達できます。ロックが調整するのは agentrec プロセス同士だけです。agentrec は各 owned worktree を削除後、ソースの `HEAD`、ステータス、インデックス、参照、ワークツリー一覧、共通リポジトリ config を事前 snapshot と比較します。drift を観測した場合は次のレグを開始せず、`1` で終了します。ただし実行も中断された場合は `130` を優先します。agentrec は drift を報告しますが、破壊的な復元は行いません。

終了コード: 使い方または事前チェックによる拒否は `2` です。ランナーの二重指定や指定漏れ、
読めないタスクファイル、汚れたチェックアウト、コミットされていない `.agentrec.yaml`、
リポジトリ内にある `AGENTREC_HOME` はいずれも、チェックアウトもプロバイダーも存在しない
段階で発生します。続いて、両方のレグが完了し、両方の検証に通れば `0`、レグが失敗した・
未完了で終わった・ソースリポジトリを変更した・チェックアウトを削除できなかった場合は `1`、
drift も観測された実行が中断された場合は `130` です。**プロバイダー自身の終了コードは
そのバンドル内の証拠であり、集約コマンドがそのまま返すことはありません。**

最終的なプロバイダー起動判断の時点で、すでに保留またはキュー済みの中断がある場合は、
そのプロバイダーを起動しません。その userspace 判断後に届いた中断は、現在のレグの
プロセスグループを停止します。POSIX におけるシグナル配送とプロセス起動は単一の atomic
操作ではありません。agentrec はそのレグの証拠を確定し、チェックアウトを削除し、次の
レグを決して起動しません。この場合、比較には実行されなかったランナーを `(not run)` と
表示します。agentrec が即時に kill された場合、たとえば `SIGKILL` やマシン停止の場合は、
残ったチェックアウトを、ソースリポジトリで `git worktree prune` を実行し、
`$AGENTREC_HOME/shadow` 以下に残るディレクトリを削除して回収します。古いワークツリーの
自動回収は行いません。

## 実行の保存場所

`$AGENTREC_HOME` が設定されていれば `$AGENTREC_HOME/runs`、設定されていなければ
`~/.local/share/agentrec/runs` に保存します。実行ディレクトリは `0700`、その内部の
すべてのファイルは `report.md` を含め `0600` で作成されます。バンドルにはプライベートな
リポジトリの内容が含まれ得るため、記録したユーザーだけが読めます。実行ごとに 1 つの
ディレクトリが `manifest.json`、`prompt.txt`、サニタイズ済みのイベントストリームと
stderr、`actions.jsonl`、`process/result.json`、`git/`（baseline、結果、追跡されていない
ファイル本文）、`verification/results.json`、`report.md` を保持します。
`provider-stdout.unparsed.log` が追加されるのは、プロバイダーが stdout にイベント以外を
出力した場合だけです。イベントだけを出力した実行には、そうでないように見せる空ファイルを
残しません。

## ステータスと終了コード

ステータスは記録された値をそのまま示し、推論で補うことはありません:

- **リポジトリ** — `AVAILABLE`（測定済み）、`UNAVAILABLE`（測定結果を取得できなかった）、`PENDING`（実行前に書き込まれ、最後まで答えを得られなかった）。カウントが表示されるのは `AVAILABLE` のときだけです。`PENDING` の実行における 0 は、*測定されていない*ことを意味し、*何もないと測定された*ことを意味しません。
- **検証** — `PASS`、`FAIL`、`TIMEOUT`、`ERROR`、`TAINTED`。検証を要求しなかった実行は `(none)` を示しますが、これはチェックに通ったという意味ではありません。
- **設定の汚染** — `--verify` は、プロバイダー開始前に `.agentrec.yaml` とその SHA-256 を固定します。実行がこのファイルを書き換えた場合、検証は `TAINTED`、理由 `config_changed` として記録され、**何も実行されず**、固定されたチェックは `PENDING` のまま残ります。

終了コード: プロバイダーが完了し、検証を要求した場合はそれにも通れば `0`、`1`–`125` は
プロバイダー自身の終了コードをそのまま渡したもの、`1` は記録・描画・検証の失敗、`2` は
agentrec の誤った呼び出し方、`130` は中断を示します。

Ctrl-C と SIGTERM は、届いた場所ですぐに処理するのではなく保留します。これはプロバイダーが
動作中だけでなく、記録処理全体を通じて行われます。agentrec はプロバイダーのプロセス
グループを停止し、manifest を閉じ、リポジトリを測定し、固定済みチェックを実行し、report を
保存して `130` で終了します。したがって、処理中のどの時点で中断された実行であっても、
`PENDING` のまま放置されず、どう終了したかを記録します。最初のシグナルが、最後に保留する
シグナルです。その後の処理はオペレーティングシステムへ戻るため、2 回目の Ctrl-C は
その場でプロセスを終了させます。

`process/result.json` は、プロセスが終了した場合は終了コードを、シグナルで kill された
場合は終了させたシグナルを記録します。シグナルで kill されたプロセスに終了コードはなく、
一方のフィールドを他方から推論することもありません。

## セキュリティ

- **保存前に構造的な秘匿を行う。** プロバイダーイベント、stderr、イベントではない stdout の行は、保存前に秘匿します。正規化した名前が 17 個の秘密サフィックス（`TOKEN`、`SECRET`、`PASSWORD`、`APIKEY`、`PASSPHRASE`、`AUTHORIZATION`、`COOKIE`、…）のいずれかで終わるフィールド内の値、さらに `NAME=VALUE` 形式の代入と 13 種類のベンダートークン形式（GitHub、OpenAI、AWS、Google、Stripe、JWT、Slack のトークンと Webhook、GitLab、npm、Hugging Face、PyPI）は、`[REDACTED:n]` に置き換えられます。部分文字列ではなくサフィックスで判定するため、`PUBLIC_KEY`、`primaryKey`、`token_id` は読み取れる状態で残ります。ルールのバージョンは manifest ごとに刻まれます。`1` と `2` が刻まれた evidence bundle は異なるルールで判定されており、秘匿件数を比較できません。
- **追跡されていないファイル本文は保存する。** 保存先は `git/untracked/` 以下で、ハッシュはサニタイズ済みテキストに対して計算します。生テキストのハッシュは、短い秘密を推測で復元させ得るためです。
- **report に生のプロバイダーイベントストリーム、追跡済みパッチ、追跡されていないファイル本文は決して埋め込まない。** report に含めるのは、プロバイダー由来の正規化済み要約です。アクションは 1 つのラベル、許可リストにある 1 つの詳細フィールド、制御文字をエスケープした固定の要約フィールドへ縮約されます。これにより、プロバイダー文字列が timeline の行を偽造したり、ターミナルを操作したりすることを防ぎます。bundle は防御的に読み戻され、シンボリックリンクはたどらず拒否し、サイズ・行長・項目数には上限があります。
- **秘匿件数が 0 でも、秘密が存在しないとは限らない。** これは、どのルールにも一致しなかったことを意味するだけです。名前のないフィールド内の秘密、代入ではなく散文内の秘密、最小長より短い秘密も、同じく 0 になります。

## 目標としないこと

- **システムコールレベルの完全性は提供しない。** エージェントの作業中にそれを観測するものはありません。agentrec が記録するのは、プロバイダーが報告した内容、実行前後でのリポジトリの見え方、そして後から独立したチェックが示した結果です。
- **リポジトリ差分は因果的な帰属ではない。** 変更が実行中に起きたことと、エージェントがその変更を行ったことは同じではありません。チェックアウトを編集する別の要因も同じ差分に現れ得ます。すべての report でもこの点を明記します。同様に、検証が通ったことは、その実行が残したツリー上で固定済みチェックが通ったことだけを示します。
- **対話セッションは記録しない。** また、ポリシーエンジン、sandbox、リモートアップロードも提供しません。agentrec は観測し、ローカルへ書き込むだけです。

**サポート対象: macOS と Linux。** プロセスグループの監督は `darwin || linux` 向けにのみ
実装されており（`internal/runner/process_unix.go`）、Windows はビルドも検証もされて
いません。

## 証拠

動作に関する主張は
[docs/dogfood/2026-07-28-evidence.md](docs/dogfood/2026-07-28-evidence.md) が裏付けています。
ここには、固定された 20 回の試行によるチェックポイントに加え、その後の実際の変更、検証の
`FAIL`、プロバイダーの非ゼロ終了、設定の `TAINTED`、中断、そしてそれらの実行が何を
確立**しない**かが含まれます。

`agentrec shadow run` の実プロバイダーによる成功経路は、
[docs/dogfood/2026-07-29-shadow-evidence.md](docs/dogfood/2026-07-29-shadow-evidence.md) が
裏付けています。macOS 上で同一コミットから Claude Code と Codex を各 1 回実行し、
固定済み検証は両方とも通過し、2 つの worktree は削除され、2 つの bundle は保持されました。
この実行は、実プロバイダーの失敗・中断経路や Linux ランタイムを確立するものではありません。
これらのライフサイクル経路は、制御された代役を用いるリポジトリテストで扱っています。

## 開発

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

`.github/workflows/release.yml` は `v*.*.*` タグで同じスクリプトを実行します。各アーカイブの
内容一覧と、そのジョブ自身がビルドしたバイナリのバージョン出力を確認してから、初めて公開
します。すでに存在するリリースに対する実行は拒否します。

## ライセンス

agentrec は [MIT ライセンス](LICENSE) の下で提供されます。サードパーティに関する表示と
依存ライブラリのライセンスは [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) にあります。
