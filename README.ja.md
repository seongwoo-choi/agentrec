<div align="center">

# agentrec

**コーディングエージェントのためのフライトレコーダー — すべての実行が、ターミナルが消えたあとでも読める、帰属情報つきのローカルな証拠バンドルを残す。**

[![CI](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml/badge.svg)](https://github.com/seongwoo-choi/agentrec/actions/workflows/ci.yml)
[![Go 1.26](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[English](README.md) | [한국어](README.ko.md) | 日本語 | [简体中文](README.zh-CN.md)

</div>

## 課題

コーディングエージェントが作業を終えたとき手元に残るのは、スクロールバックだけ
だ。それは流れて消え、エージェントが行ったと*言った*ことと実際に起きたことを
混ぜてしまい、リポジトリがまだビルドできるかについては何も語らない。

agentrec は、非対話モードの Claude Code または Codex の実行 1 回をバンドルとして
記録する。そのバンドルには、正規化されたアクションのタイムライン、監督下にあった
プロセスの結果、実行区間の前後でのリポジトリの差分、そしてリポジトリ自身が固定した
チェックの結果が含まれる。それぞれは異なる観測者に由来し、バンドルはそれらを分けて
保持する。

## 4 つの証拠レイヤー

| レイヤー | 観測者 | 意味するもの | 記録される帰属 |
|---|---|---|---|
| **プロバイダー報告アクション** | エージェント | エージェントが行ったと言ったこと — ツール呼び出し、シェルコマンド、ファイルの読み取りと編集、MCP 呼び出し、Codex のファイル変更。正規化して要約するが、証明としては決して扱わない。 | `provider_reported` |
| **スーパーバイザー観測結果** | agentrec | プロバイダープロセスがどう終了したか: 終了コード、終了理由、シグナル、所要時間、警告数。 | `supervisor_observed` |
| **リポジトリ観測変更** | agentrec | 実行前に固定したコミットと、実行後のワークツリーとの差分を、agentrec 自身が測定したもの。 | `observed during run, not causal proof` |
| **検証観測結果** | agentrec | プロバイダーが停止したあとに agentrec がリポジトリ自身の固定されたチェックを実行したとき、そのチェックがどう終了したか。作業がどう行われたかについては何も語らない。 | `verification_observed` |

プロバイダーの進捗、共同作業の待ち、TODO リストのライフサイクルだけを運ぶ
イベントはストリームのメタデータである。これらはいかなるアクションも指し示さず、
警告数を膨らませない。

## クイックスタート

**前提条件。** ソースからビルドするには Go 1.26 以降が必要である。サポートされる
プロバイダー CLI が `PATH` 上にすでに存在していなければならない。agentrec はそれ
を起動するだけで、インストールはしない。サポート範囲外のバージョンは、イベント
ストリームがまだ合致するだろうという前提で記録されることはなく、拒否される。

| プロバイダー | 実行ファイル | サポート範囲 | 備考 |
|---|---|---|---|
| Claude Code | `claude` | `>=2.1.0, <3.0.0` | `-p`/`--print` が必要。agentrec が `--output-format stream-json --verbose --include-hook-events` を注入する。 |
| Codex | `codex` | `>=0.144.0, <1.0.0` | `exec` が最初の引数でなければならない。agentrec が `--json` を注入する。 |

各タグ付きリリースには 4 つのアーカイブ — `darwin_amd64`、`darwin_arm64`、
`linux_amd64`、`linux_arm64` — と、その 4 つすべてを対象とする `SHA256SUMS`
ファイルが含まれる。アーカイブを展開すると、`agentrec`、`LICENSE`、
`THIRD_PARTY_NOTICES.md`、`third_party/licenses/Apache-2.0.txt` を収めた
ディレクトリが 1 つ現れる。

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

`agentrec version`（同等に `agentrec --version`）は 3 行を出力する。バージョン、
ビルド元のコミット、そして UTC のビルド時刻である。リリースバイナリはタグ、完全な
コミット SHA、RFC 3339 のタイムスタンプを持つ。それ以外の方法で作られたビルドは
`dev`、`unknown`、`unknown` を報告するので、スタンプのないバイナリがリリース済みの
ものと取り違えられることはない。

**検証設定をコミットする。** 実行が検証されるのは、リポジトリがすでに持っていた
チェックに対してのみである。`.agentrec.example.yaml` を `.agentrec.yaml` にコピー
してコミットする。各コマンドはシェルを介さず直接起動されるので、引数は引数であって
それ以上のものではない:

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
操作もない Git チェックアウトでなければならない。そうすることで、その実行自身の
変更を区別できる。リポジトリごとに同時に 1 実行のみ。2 つ目はキューに入らず拒否
される。

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

`agentrec list` は実行を新しい順に出力し、`PROJECT` 列はマニフェストが記録した作業
ディレクトリの末尾要素から取られる。絶対パス以外を保持するマニフェストは、推測する
のではなく `unknown` を報告する。

`--cwd` が一致するのは前方一致ではなく、**ちょうど 1 つのディレクトリ**である。
与えられたパスは絶対化され正規化され、マニフェスト自身の作業ディレクトリ — これも
絶対パスで、同じやり方で正規化される — がちょうどそれと同じであるときにのみ、その
実行は残る。サブディレクトリは別のパスであり、シンボリックリンク経由の別の経路も
同様である。

## レポートの見え方

`agentrec show` は読み取り専用である。バンドルから実行 1 件を描画するだけで、何も
書き込まない。実際に記録された実行からの抜粋である（`582ee874`、アクション 1 件に
切り詰めたもの）:

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

`agentrec trace` は、何かを出力する前に、同じバンドルの同じ読み取り結果を
`<run>/report.md` に書く。それは一度きりで二度と行われない。その名前ですでに存在
しているレポートは、上書きされるのではなく拒否される。

## 1 つのタスクで 2 つのエージェントを比較する

`agentrec shadow run` は 1 つのタスクを 2 回 — 一度は Claude Code で、一度は Codex
で — 単一のコミット済みベースラインから記録し、記録された 2 つの実行を並べて出力
する:

```bash
agentrec shadow run task.md --runner claude --runner codex
```

各レグは、ソースリポジトリの `HEAD` から作られた使い捨ての**デタッチド Git
ワークツリー**の中で記録される。場所は `$AGENTREC_HOME/shadow/<group>/<runner>`、
モードは `0700` で、そのレグの証拠が閉じられた時点で削除される。両方のレグは通常の
実行バンドルを残すので、チェックアウトが消えたあとでも `agentrec list` と
`agentrec show <run-id>` で読み戻せる。比較そのものは標準出力に出力され、各レグの
永続的な `report.md` はそれぞれのバンドルに残る。

比較はランナーごとに 1 ブロックを、常にこの順序で、そしてこれらのフィールドをこの
順序で出力する — 実行 ID、チェックがどう終わり何に固定されていたか、プロセスがどう
終わったか、実行が自分のチェックアウトに何を残したか、そしてどれだけのことをしたか:

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

このコマンドが与えるものと、与えないもの:

- **隔離は干渉を狭めるが、因果的な帰属ではない。** 各レグのリポジトリ差分は依然と
  して `observed during run, not causal proof` として記録される。
- **スコアも、勝者も、推奨もない。** 比較は記録されたフィールドを示すだけで、そこ
  から導出したものは何も示さない。どちらの実行を選ぶかは読み手の判断である。
  プロバイダーが報告するコストやトークンのフィールドは現時点の記録済み証拠に含まれ
  ていないため、比較はそれらを表示しない。
- **バイト単位で hermetic な sandbox ではなく、Git のチェックアウトである。**
  追跡されていない `.env` ファイルやローカル資格情報はレグへコピーされない。追跡
  ファイルは利用者の Git がチェックアウトするため、設定済みの attribute、filter、
  hook はそのまま適用される。agentrec は資格情報の輸送やワークスペース準備を追加しない。
- **準備できないリポジトリは、中途半端に準備されるのではなく拒否される。**
  コミットされた `.gitmodules`、またはコミットされた Git LFS のポインタファイルは、
  チェックアウトが 1 つでも存在する前に拒絶される。
- **タスクは 1 つのコマンドライン引数である。** タスクファイルは一度だけ読まれる —
  最大 64 KiB の、シンボリックリンクでない通常の UTF-8 ファイル 1 つ — そして各
  エージェントに `claude -p -- <task>` と `codex exec --json -- <task>` として渡される。
  標準入力で与えるプロンプトや、複数の引数に分けたプロンプトは、ここではサポートされない。
- **検証は必須であり、レグは直列化される。** 両方の実行はコミットされた
  `.agentrec.yaml` に対して検証され、片方ずつ順に実行される。チェックは重ならないが、
  可変な認証、キャッシュ、ネットワークサービスなどの外部状態はレグ間で初期化されない。
  そのため入力したランナー順が 2 番目のプロバイダーの観測に影響しうる。
- **リンクされたワークツリーはセキュリティ境界ではない。** 共通 Git ディレクトリと参照を
  共有し、プロバイダーはソースチェックアウトへ明示的に到達できる。ロックが調整するのは
  agentrec プロセス同士だけである。agentrec は各 owned worktree を削除したあと、ソースの
  `HEAD`、ステータス、インデックス、参照、ワークツリー一覧、共通リポジトリ config を
  事前 snapshot と比較する。観測された drift があれば次のレグを始めず `1` で終了する。
  ただし実行も中断された場合は `130` が優先される。agentrec は drift を報告するが
  破壊的に復元しない。

終了コード: 使い方または事前チェックによる拒否は `2` — ランナーの二重指定や指定
漏れ、読めないタスクファイル、汚れたチェックアウト、コミットされていない
`.agentrec.yaml`、リポジトリ内部にある `AGENTREC_HOME` — これらはいずれも、
チェックアウトもプロバイダーも存在しないうちに起こる。続いて、両方のレグが完了し
両方の検証が通れば `0`、レグが失敗した・未完了で終わった・ソースリポジトリを変更した・
チェックアウトを削除できなかった場合は `1`、drift も観測されたとしても実行が中断された
場合は `130` である。**プロバイダー自身の終了
コードはそのバンドル内の証拠であり、集約コマンドがそれをそのまま通すことは決して
ない。**

最終的なプロバイダー起動判断の時点ですでに保持またはキューされている中断は、その
プロバイダーの起動を防ぐ。その userspace 判断後に届いた中断は現在のレグのプロセス
グループを停止する。POSIX のシグナル配送とプロセス起動は単一の atomic 操作ではない。
agentrec はそのレグの証拠を確定し、チェックアウトを削除し、次のレグを決して起動しない。
そのとき比較は、実行されなかったランナーを `(not run)` として示す。agentrec が即座に殺された
場合 — `SIGKILL`、またはマシンの停止 — 残されたチェックアウトは、ソースリポジトリで
`git worktree prune` を実行し、`$AGENTREC_HOME/shadow` の下に残ったディレクトリを
削除することで回収する。古いワークツリーの自動回収は行われない。

## 実行の保存場所

`$AGENTREC_HOME` が設定されていればその下の `$AGENTREC_HOME/runs`、そうでなければ
`~/.local/share/agentrec/runs` である。実行ディレクトリは `0700` で、その中のすべて
のファイルは `report.md` を含め `0600` で作成される — バンドルはプライベートな
リポジトリを引用しうるので、それを記録したユーザーだけが読める。実行ごとに 1 つの
ディレクトリが `manifest.json`、`prompt.txt`、サニタイズ済みのイベントストリームと
stderr、`actions.jsonl`、`process/result.json`、`git/`（ベースライン、結果、追跡
されていないファイルの本文）、`verification/results.json`、`report.md` を保持する。

## ステータスと終了コード

ステータスは記録されたとおりに示され、決して推論されない:

- **リポジトリ** — `AVAILABLE`（測定済み）、`UNAVAILABLE`（測定が得られなかった）、
  `PENDING`（実行前に書かれ、ついに答えられなかった）。カウントが表示されるのは
  `AVAILABLE` のときだけである。`PENDING` の実行の 0 は*測定されていない*という意味
  であって、*何もないと測定された*という意味ではない。
- **検証** — `PASS`、`FAIL`、`TIMEOUT`、`ERROR`、`TAINTED`。検証を要求しなかった
  実行は `(none)` を示すが、これは通ったチェックではない。
- **設定の汚染** — `--verify` はプロバイダーの開始前に `.agentrec.yaml` とその
  SHA-256 を固定する。実行がそのファイルを書き換えた場合、検証は `TAINTED`、理由
  `config_changed` として記録され、**何も実行されず**、固定されたチェックは
  `PENDING` のまま残る。

終了コード: `0` プロバイダーが完了し、検証があればそれが通った、`1`–`125`
プロバイダー自身の終了コードがそのまま渡された、`1` 記録・描画または検証が失敗した、
`2` agentrec の呼び出し方が誤っていた、`130` 中断された。

Ctrl-C と SIGTERM はどちらも、届いた場所で従うのではなく保留される。しかもプロバイ
ダーが動いている間だけでなく、記録全体にわたってそうである。agentrec はプロバイダー
のプロセスグループを停止し、マニフェストを閉じ、リポジトリを測定し、固定された
チェックを実行し、レポートを保存して `130` で終了する — そのため、この一連のどの
時点で中断された実行も、`PENDING` のまま放置されるのではなく、どう終わったかを語る。
最初のシグナルが最後に保留されるシグナルである。その後の扱いはオペレーティング
システムに戻るので、2 度目の Ctrl-C はプロセスをその場で終わらせる。

`process/result.json` は、プロセスが終了したときは終了コードを、シグナルで殺された
ときは終了させたシグナルを記録する。シグナルで殺されたプロセスに終了コードはなく、
どちらのフィールドも他方から推論されることはない。

## セキュリティ

- **永続化の前に構造的な秘匿を行う。** プロバイダーのイベントと stderr は、書き出さ
  れる前に秘匿される。正規化した形が 13 個の秘密サフィックス（`TOKEN`、`SECRET`、
  `PASSWORD`、`APIKEY`、`AUTHORIZATION`、`COOKIE`、…）のいずれかで終わるフィールド
  名の下にある値、加えて `NAME=VALUE` の代入とトークン形状は `[REDACTED:n]` になる。
  ルールのバージョンはマニフェストごとに刻まれる。
- **追跡されていないファイルの本文は保存される。** 場所は `git/untracked/` の下で、
  ハッシュはサニタイズ済みのテキストに対して取られる — 生のテキストのハッシュは、
  短い秘密を推測によって取り戻させてしまうからである。
- **レポートは、生のプロバイダーイベントストリーム、追跡されているパッチ、追跡され
  ていないファイルの本文を決して埋め込まない。** レポートが持つのは、プロバイダーに
  由来する正規化された要約である。アクションは 1 つのラベル、許可リストにある 1 つの
  詳細フィールド、そして制御文字をエスケープした固定の要約フィールドへと縮約される
  ので、いかなるプロバイダー文字列もタイムラインの行を偽造したり、ターミナルを操った
  りできない。バンドルは防御的に読み戻され、シンボリックリンクは辿るのではなく拒否
  され、サイズ・行長・項目数には上限がある。
- **秘匿件数が 0 であることは、秘密が存在しないという主張ではない。** それはどの
  ルールも一致しなかったという意味である — 名前のないフィールドの中の秘密、代入では
  なく散文の中の秘密、あるいは最小長より短い秘密も、同じ 0 を生む。

## 目標としないこと

- **システムコールレベルで完全ではない。** エージェントが作業している間、それを観測
  するものは何もない。agentrec が記録するのは、プロバイダーが報告したこと、実行の
  前後でリポジトリがどう見えたか、そしてそのあとに独立したチェックが何を言ったか
  である。
- **リポジトリの差分は因果的な帰属ではない。** その変更は実行中に起きた。それは
  エージェントがそれを行ったということと同じではない。チェックアウトを編集する他の
  何かも同じ差分に現れ、すべてのレポートがそう述べている。同様に、通った検証が語る
  のは、その実行が残したツリーの上で固定されたチェックが通った、ということだけで
  ある。
- **対話セッションは記録されない。**また、ポリシーエンジンもサンドボックスもリモート
  アップロードもない — agentrec は観測し、ローカルに書く。

**サポート範囲: macOS と Linux。** プロセスグループの監督は `darwin || linux` の
ためだけに作られており（`internal/runner/process_unix.go`）、Windows はビルドされて
おらず検証もされていない。

## 証拠

挙動に関する主張は
[docs/dogfood/2026-07-28-evidence.md](docs/dogfood/2026-07-28-evidence.md) が裏づけ
ている — 固定された 20 回の試行によるチェックポイントに加えて、その後の実際の変更を
含み、検証の `FAIL`、プロバイダーの非ゼロ終了、設定の `TAINTED`、中断、そしてそれら
の実行が確立**しない**ことを扱っている。

`agentrec shadow run` の実プロバイダーによる成功経路は、
[docs/dogfood/2026-07-29-shadow-evidence.md](docs/dogfood/2026-07-29-shadow-evidence.md)
が裏づけている。macOS 上で同じコミットから Claude Code と Codex を 1 回実行し、固定
された検証は両方とも通過し、2 つの worktree は削除され、2 つのバンドルは保持された。
この実行は、実プロバイダーの失敗・中断経路や Linux ランタイムを確立するものではない。
それらのライフサイクル経路は、制御された代役を使うリポジトリテストが扱っている。

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

`.github/workflows/release.yml` は `v*.*.*` タグで同じスクリプトを実行し、各アーカ
イブの内容一覧と、自身がビルドしたバイナリのバージョン出力を確認し、そのうえで
初めて公開する。すでに存在するリリースに対しては実行を拒否する。

## ライセンス

agentrec は [MIT ライセンス](LICENSE)の下で提供される。サードパーティの表示と依存
ライブラリのライセンスは
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) に保存されている。
