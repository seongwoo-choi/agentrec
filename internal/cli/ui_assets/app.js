(() => {
  'use strict';

  const POLL_MS = 5000;
  const state = { lang: 'en', runs: [], run: null, mode: 'actions', query: '', activeTypes: new Set(), selected: null, streams: null, searchTimer: null, loadGeneration: 0, runAbortController: null, pollTimer: null, pollController: null, runsSignature: '' };
  const $ = (id) => document.getElementById(id);
  const node = (tag, className, text) => {
    const el = document.createElement(tag);
    if (className) el.className = className;
    if (text !== undefined) el.textContent = text;
    return el;
  };

  // ── Localization ──────────────────────────────────────────────────────────
  // English strings are the keys. Only page-authored copy and the server sentences special-cased below are translated;
  // provider content (commands, paths, prompts, ids) and the documented status tokens (UNAVAILABLE, PASS, …) never are.
  const LANGS = ['en', 'ko', 'ja', 'zh-CN'];
  const STRINGS = {
    ko: {
      'Action Timeline': '액션 타임라인',
      'Loading recorded evidence…': '기록된 증거를 불러오는 중…',
      'Recorded runs': '기록된 실행',
      'Find a run or project': '실행 또는 프로젝트 검색',
      Request: '요청',
      'No recorded request.': '기록된 요청이 없습니다.',
      Actions: '액션',
      Changes: '변경',
      'Provider events': '프로바이더 이벤트',
      'Filter timeline': '타임라인 필터',
      'Evidence inspector': '증거 인스펙터',
      'Select an action, change, or provider event to inspect its sanitized evidence.': '액션, 변경, 프로바이더 이벤트를 선택하면 정제된 증거를 확인할 수 있습니다.',
      Language: '언어',
      'unknown project': '알 수 없는 프로젝트',
      unknown: '알 수 없음',
      'No runs recorded yet — start a Claude Code or Codex session; it appears here when it ends.': '기록된 실행이 아직 없습니다. Claude Code 또는 Codex 세션을 시작하면 종료 시 여기에 표시됩니다.',
      'No runs match this search.': '검색 결과가 없습니다.',
      'No runs recorded yet': '기록된 실행이 없습니다',
      'No run selected': '선택된 실행이 없습니다',
      'Start a Claude Code or Codex session; it appears here when it ends.': 'Claude Code 또는 Codex 세션을 시작하면 종료 시 여기에 표시됩니다.',
      'Pick a run from the list to inspect its recorded evidence.': '목록에서 실행을 선택하면 기록된 증거를 확인할 수 있습니다.',
      'No recorded runs': '기록된 실행 없음',
      '{n} recorded run(s)': '기록된 실행 {n}개',
      'Could not load recorded runs': '기록된 실행을 불러오지 못했습니다',
      'Could not load the latest run': '최신 실행을 불러오지 못했습니다',
      '{error} — retrying; pick a run from the list to try another.': '{error} — 다시 시도 중입니다. 목록에서 다른 실행을 선택할 수도 있습니다.',
      '{n} unreadable run(s) were excluded.': '읽을 수 없는 실행 {n}개를 제외했습니다.',
      '{n}s ago': '{n}초 전',
      '{n}m ago': '{n}분 전',
      '{n}h ago': '{n}시간 전',
      '{n}d ago': '{n}일 전',
      'No checks were run for this session. Record with --verify (agentrec setup --verify) to run the checks pinned in .agentrec.yaml.': '이 세션에서는 검증 체크를 실행하지 않았습니다. --verify 로 기록하면(agentrec setup --verify) .agentrec.yaml 에 고정된 체크를 실행합니다.',
      'agentrec did not launch this session, so exit code and signal were never seen.': 'agentrec이 이 세션을 직접 실행하지 않았으므로 종료 코드와 시그널을 관측하지 못했습니다.',
      'The end was reported by the provider\'s SessionEnd hook.': '종료는 프로바이더의 SessionEnd 훅이 보고했습니다.',
      'The diff is measured when the session ends.': 'diff는 세션이 끝난 뒤 측정합니다.',
      'No repository diff was recorded for this run.': '이 실행에서는 저장소 diff를 기록하지 않았습니다.',
      'Observed during the run — this is not proof the agent caused it': '실행 중에 관측된 것으로, 에이전트가 원인이라는 증명은 아닙니다',
      'The repository diff artifacts are incomplete (git/result.json is missing).': '저장소 diff 아티팩트가 불완전합니다(git/result.json 없음).',
      'The repository diff artifacts are incomplete (the change list is missing).': '저장소 diff 아티팩트가 불완전합니다(변경 목록 없음).',
      'Reported by {provider}': '{provider}가 보고',
      'Observed by agentrec': 'agentrec이 관측',
      'Observed by verification checks': '검증 체크가 관측',
      'The provider did not report usage for this run.': '프로바이더가 이 실행의 사용량을 보고하지 않았습니다.',
      'No process result was recorded.': '프로세스 결과를 기록하지 않았습니다.',
      'Session is still open; results appear when it ends.': '세션이 아직 열려 있습니다. 결과는 종료 후 표시됩니다.',
      'The recorder ended without writing how the session ended.': '레코더가 세션 종료 사유를 기록하지 못한 채 끝났습니다.',
      'Ended by the provider\'s SessionEnd hook, as reported.': '프로바이더의 SessionEnd 훅이 보고한 종료입니다.',
      'No hook arrived for the idle timeout, or the recorder was signalled; the session\'s own end was not seen.': '유휴 시간 초과 동안 훅이 오지 않았거나 레코더가 시그널을 받았습니다. 세션의 실제 종료는 관측하지 못했습니다.',
      'The provider process exited normally.': '프로바이더 프로세스가 정상 종료되었습니다.',
      'The provider process exited with a non-zero code.': '프로바이더 프로세스가 0이 아닌 코드로 종료되었습니다.',
      'The run was interrupted before the provider finished.': '프로바이더가 끝나기 전에 실행이 중단되었습니다.',
      'The run hit its time limit and the provider was stopped.': '실행이 제한 시간에 도달해 프로바이더를 중지했습니다.',
      'exit code {n}': '종료 코드 {n}',
      'signal {s}': '시그널 {s}',
      'Duration unavailable': '소요 시간 없음',
      '{passed} of {total} checks passed.': '체크 {total}개 중 {passed}개 통과.',
      '{passed} of {total} checks passed, {failed} failed.': '체크 {total}개 중 {passed}개 통과, {failed}개 실패.',
      'Every pinned check passed.': '고정된 체크가 모두 통과했습니다.',
      'At least one pinned check failed.': '고정된 체크 중 하나 이상이 실패했습니다.',
      'A check was still running when its time limit ran out.': '체크가 제한 시간 안에 끝나지 않았습니다.',
      'A check could not be run to completion.': '체크를 끝까지 실행하지 못했습니다.',
      'The pinned configuration changed under the run, so no checks were executed.': '실행 도중 고정된 설정이 바뀌어 체크를 실행하지 않았습니다.',
      '{tracked} tracked · {untracked} untracked · +{additions}/−{deletions} · {binary} binary': '추적 {tracked} · 미추적 {untracked} · +{additions}/−{deletions} · 바이너리 {binary}',
      'No checks were run for this session.': '이 세션에서는 검증 체크를 실행하지 않았습니다.',
      'Verification checks passed.': '검증 체크를 통과했습니다.',
      'Verification checks failed.': '검증 체크가 실패했습니다.',
      'The run ended with {label}.': '실행이 {label}(으)로 끝났습니다.',
      'Previous page': '이전 페이지',
      'Next page': '다음 페이지',
      '{n} items on this page': '이 페이지 {n}개',
      'bounded patch page': '패치 페이지(크기 제한)',
      'Could not load actions: {error}': '액션을 불러오지 못했습니다: {error}',
      'No loaded actions match this filter.': '필터에 맞는 액션이 없습니다.',
      'No actions were recorded for this run.': '이 실행에는 기록된 액션이 없습니다.',
      'Loading actions…': '액션 불러오는 중…',
      'Could not load repository changes: {error}': '저장소 변경을 불러오지 못했습니다: {error}',
      'Repository change evidence is unavailable.': '저장소 변경 증거를 사용할 수 없습니다.',
      'Loading repository changes…': '저장소 변경 불러오는 중…',
      'No repository changes were observed.': '관측된 저장소 변경이 없습니다.',
      'No loaded changes match this filter.': '필터에 맞는 변경이 없습니다.',
      'Could not load provider events: {error}': '프로바이더 이벤트를 불러오지 못했습니다: {error}',
      '(untyped)': '(유형 없음)',
      'event {n}': '이벤트 {n}',
      'provider event': '프로바이더 이벤트',
      'No loaded provider events match this filter.': '필터에 맞는 프로바이더 이벤트가 없습니다.',
      'This run has no sanitized provider-event artifact.': '이 실행에는 정제된 프로바이더 이벤트 아티팩트가 없습니다.',
      'Loading provider events…': '프로바이더 이벤트 불러오는 중…',
      'same path observed — not causal proof': '같은 경로가 관측됨 — 인과 증명은 아님',
      reported: '보고됨',
      provider: '프로바이더',
      '{kind} selected: {label}': '{kind} 선택됨: {label}',
      prompt: '요청',
      reply: '응답',
      You: '나',
      'Show more': '더 보기',
      'Show less': '접기',
      '… full text in the inspector': '… 전체 내용은 인스펙터에서 확인',
      '(untyped event)': '(유형 없는 이벤트)',
      'SAME PATH OBSERVED — NOT CAUSAL PROOF': '같은 경로 관측 — 인과 증명 아님',
      'SANITIZED INPUT': '정제된 입력',
      'SANITIZED RESULT': '정제된 결과',
      'MESSAGE TEXT': '메시지 본문',
      tracked: '추적됨',
      untracked: '미추적',
      binary: '바이너리',
      'REPOSITORY-OBSERVED METADATA': '저장소 관측 메타데이터',
      'SANITIZED REPOSITORY PATCH': '정제된 저장소 패치',
      'Loading patch…': '패치 불러오는 중…',
      'No patch bytes on this page.': '이 페이지에 패치 내용이 없습니다.',
      'event #{n}': '이벤트 #{n}',
      'SANITIZED PROVIDER EVENT': '정제된 프로바이더 이벤트',
      'Loading patch for {path}': '{path} 패치 불러오는 중',
      'Patch loaded for {path}': '{path} 패치를 불러왔습니다',
      'Patch unavailable: {error}': '패치를 사용할 수 없습니다: {error}',
      'Provider usage': '프로바이더 사용량',
      'Process result': '프로세스 결과',
      'Repository delta': '저장소 변경분',
      Verification: '검증',
      Unavailable: '없음',
      Status: '상태',
      Session: '세션',
      Provider: '프로바이더',
      'Exit Reason': '종료 사유',
      'Ended By': '종료 주체',
      Duration: '소요 시간',
      Warnings: '경고',
      Files: '파일',
      'Stored Text': '저장된 텍스트',
      Baseline: '기준점',
      Attribution: '출처',
      Window: '측정 구간',
      Pinned: '고정',
      Reason: '사유',
      'Exit Code': '종료 코드',
      Signal: '시그널',
      Check: '체크',
      Warning: '경고',
      Config: '설정',
      Version: '버전',
      Scope: '범위',
      Unparsed: '미해석',
      'Cost USD': '비용(USD)',
      'the provider\'s SessionEnd hook, as reported; agentrec did not observe the process end': '프로바이더의 SessionEnd 훅(보고된 대로). agentrec은 프로세스 종료를 관측하지 않았습니다',
      'the recorder, after no hook delivery for the idle timeout or on a signal; the session\'s own end was not seen': '레코더(유휴 시간 초과 동안 훅 미수신 또는 시그널 수신). 세션의 실제 종료는 관측하지 못했습니다',
      'nothing yet: the session is still open and its recorder is running': '아직 없음: 세션이 열려 있고 레코더가 실행 중입니다',
      'baseline pinned at the SessionStart hook, not before the process started; measured after the session ended; the checkout was open to the operator in between': '기준점은 프로세스 시작 전이 아니라 SessionStart 훅 시점에 고정했고, 세션 종료 후 측정했습니다. 그 사이 체크아웃은 운영자에게 열려 있었습니다',
      'at the SessionStart hook; run after the session ended': 'SessionStart 훅 시점에 고정, 세션 종료 후 실행',
      'Process outcome': '프로세스 결과',
      'Verification verdict': '검증 판정',
      'Repository evidence': '저장소 증거',
      'Normalized actions': '정규화된 액션',
      'Run {run} · Verify {verify}': '실행 {run} · 검증 {verify}',
      'unknown cwd': '알 수 없는 작업 디렉터리',
      '{provider} {version} is unsupported; this timeline may be incomplete.': '{provider} {version}은(는) 지원하지 않는 버전입니다. 이 타임라인은 불완전할 수 있습니다.',
      'unknown version': '알 수 없는 버전'
    },
    ja: {
      'Action Timeline': 'アクションタイムライン',
      'Loading recorded evidence…': '記録された証跡を読み込んでいます…',
      'Recorded runs': '記録された実行',
      'Find a run or project': '実行またはプロジェクトを検索',
      Request: 'リクエスト',
      'No recorded request.': '記録されたリクエストはありません。',
      Actions: 'アクション',
      Changes: '変更',
      'Provider events': 'プロバイダーイベント',
      'Filter timeline': 'タイムラインを絞り込む',
      'Evidence inspector': '証跡インスペクター',
      'Select an action, change, or provider event to inspect its sanitized evidence.': 'アクション、変更、プロバイダーイベントを選択すると、サニタイズ済みの証跡を確認できます。',
      Language: '言語',
      'unknown project': '不明なプロジェクト',
      unknown: '不明',
      'No runs recorded yet — start a Claude Code or Codex session; it appears here when it ends.': 'まだ記録された実行はありません。Claude Code または Codex のセッションを開始すると、終了時にここに表示されます。',
      'No runs match this search.': '検索に一致する実行はありません。',
      'No runs recorded yet': '記録された実行はありません',
      'No run selected': '実行が選択されていません',
      'Start a Claude Code or Codex session; it appears here when it ends.': 'Claude Code または Codex のセッションを開始すると、終了時にここに表示されます。',
      'Pick a run from the list to inspect its recorded evidence.': '一覧から実行を選ぶと、記録された証跡を確認できます。',
      'No recorded runs': '記録された実行なし',
      '{n} recorded run(s)': '記録された実行 {n} 件',
      'Could not load recorded runs': '記録された実行を読み込めませんでした',
      'Could not load the latest run': '最新の実行を読み込めませんでした',
      '{error} — retrying; pick a run from the list to try another.': '{error} — 再試行しています。一覧から別の実行を選ぶこともできます。',
      '{n} unreadable run(s) were excluded.': '読み取れない実行 {n} 件を除外しました。',
      '{n}s ago': '{n}秒前',
      '{n}m ago': '{n}分前',
      '{n}h ago': '{n}時間前',
      '{n}d ago': '{n}日前',
      'No checks were run for this session. Record with --verify (agentrec setup --verify) to run the checks pinned in .agentrec.yaml.': 'このセッションでは検証チェックを実行していません。--verify を付けて記録すると（agentrec setup --verify）、.agentrec.yaml に固定されたチェックを実行します。',
      'agentrec did not launch this session, so exit code and signal were never seen.': 'agentrec がこのセッションを起動していないため、終了コードとシグナルは観測されていません。',
      'The end was reported by the provider\'s SessionEnd hook.': '終了はプロバイダーの SessionEnd フックが報告しました。',
      'The diff is measured when the session ends.': 'diff はセッション終了後に計測します。',
      'No repository diff was recorded for this run.': 'この実行ではリポジトリの diff を記録していません。',
      'Observed during the run — this is not proof the agent caused it': '実行中に観測されたもので、エージェントが原因であることの証明ではありません',
      'The repository diff artifacts are incomplete (git/result.json is missing).': 'リポジトリ diff の成果物が不完全です（git/result.json がありません）。',
      'The repository diff artifacts are incomplete (the change list is missing).': 'リポジトリ diff の成果物が不完全です（変更一覧がありません）。',
      'Reported by {provider}': '{provider} が報告',
      'Observed by agentrec': 'agentrec が観測',
      'Observed by verification checks': '検証チェックが観測',
      'The provider did not report usage for this run.': 'プロバイダーはこの実行の使用量を報告していません。',
      'No process result was recorded.': 'プロセス結果は記録されていません。',
      'Session is still open; results appear when it ends.': 'セッションはまだ開いています。結果は終了後に表示されます。',
      'The recorder ended without writing how the session ended.': 'レコーダーはセッションの終了理由を書き残さずに終了しました。',
      'Ended by the provider\'s SessionEnd hook, as reported.': 'プロバイダーの SessionEnd フックが報告した終了です。',
      'No hook arrived for the idle timeout, or the recorder was signalled; the session\'s own end was not seen.': 'アイドルタイムアウトの間フックが届かなかったか、レコーダーがシグナルを受け取りました。セッション自体の終了は観測されていません。',
      'The provider process exited normally.': 'プロバイダーのプロセスは正常に終了しました。',
      'The provider process exited with a non-zero code.': 'プロバイダーのプロセスは 0 以外のコードで終了しました。',
      'The run was interrupted before the provider finished.': 'プロバイダーが完了する前に実行が中断されました。',
      'The run hit its time limit and the provider was stopped.': '実行が制限時間に達したため、プロバイダーを停止しました。',
      'exit code {n}': '終了コード {n}',
      'signal {s}': 'シグナル {s}',
      'Duration unavailable': '所要時間なし',
      '{passed} of {total} checks passed.': 'チェック {total} 件中 {passed} 件が合格。',
      '{passed} of {total} checks passed, {failed} failed.': 'チェック {total} 件中 {passed} 件が合格、{failed} 件が不合格。',
      'Every pinned check passed.': '固定されたチェックはすべて合格しました。',
      'At least one pinned check failed.': '固定されたチェックのうち 1 件以上が不合格でした。',
      'A check was still running when its time limit ran out.': 'チェックが制限時間内に終わりませんでした。',
      'A check could not be run to completion.': 'チェックを最後まで実行できませんでした。',
      'The pinned configuration changed under the run, so no checks were executed.': '実行中に固定された設定が変わったため、チェックは実行されませんでした。',
      '{tracked} tracked · {untracked} untracked · +{additions}/−{deletions} · {binary} binary': '追跡 {tracked} · 未追跡 {untracked} · +{additions}/−{deletions} · バイナリ {binary}',
      'No checks were run for this session.': 'このセッションでは検証チェックを実行していません。',
      'Verification checks passed.': '検証チェックに合格しました。',
      'Verification checks failed.': '検証チェックに不合格でした。',
      'The run ended with {label}.': '実行は {label} で終了しました。',
      'Previous page': '前のページ',
      'Next page': '次のページ',
      '{n} items on this page': 'このページ {n} 件',
      'bounded patch page': 'パッチページ（サイズ上限あり）',
      'Could not load actions: {error}': 'アクションを読み込めませんでした: {error}',
      'No loaded actions match this filter.': 'フィルターに一致するアクションはありません。',
      'No actions were recorded for this run.': 'この実行には記録されたアクションがありません。',
      'Loading actions…': 'アクションを読み込んでいます…',
      'Could not load repository changes: {error}': 'リポジトリの変更を読み込めませんでした: {error}',
      'Repository change evidence is unavailable.': 'リポジトリ変更の証跡は利用できません。',
      'Loading repository changes…': 'リポジトリの変更を読み込んでいます…',
      'No repository changes were observed.': '観測されたリポジトリの変更はありません。',
      'No loaded changes match this filter.': 'フィルターに一致する変更はありません。',
      'Could not load provider events: {error}': 'プロバイダーイベントを読み込めませんでした: {error}',
      '(untyped)': '(型なし)',
      'event {n}': 'イベント {n}',
      'provider event': 'プロバイダーイベント',
      'No loaded provider events match this filter.': 'フィルターに一致するプロバイダーイベントはありません。',
      'This run has no sanitized provider-event artifact.': 'この実行にはサニタイズ済みのプロバイダーイベント成果物がありません。',
      'Loading provider events…': 'プロバイダーイベントを読み込んでいます…',
      'same path observed — not causal proof': '同じパスを観測 — 因果の証明ではありません',
      reported: '報告済み',
      provider: 'プロバイダー',
      '{kind} selected: {label}': '{kind} を選択: {label}',
      prompt: 'プロンプト',
      reply: '返答',
      You: '自分',
      'Show more': 'もっと見る',
      'Show less': '閉じる',
      '… full text in the inspector': '… 全文はインスペクターで確認',
      '(untyped event)': '(型なしイベント)',
      'SAME PATH OBSERVED — NOT CAUSAL PROOF': '同じパスを観測 — 因果の証明ではない',
      'SANITIZED INPUT': 'サニタイズ済み入力',
      'SANITIZED RESULT': 'サニタイズ済み結果',
      'MESSAGE TEXT': 'メッセージ本文',
      tracked: '追跡中',
      untracked: '未追跡',
      binary: 'バイナリ',
      'REPOSITORY-OBSERVED METADATA': 'リポジトリ観測メタデータ',
      'SANITIZED REPOSITORY PATCH': 'サニタイズ済みリポジトリパッチ',
      'Loading patch…': 'パッチを読み込んでいます…',
      'No patch bytes on this page.': 'このページにパッチの内容はありません。',
      'event #{n}': 'イベント #{n}',
      'SANITIZED PROVIDER EVENT': 'サニタイズ済みプロバイダーイベント',
      'Loading patch for {path}': '{path} のパッチを読み込んでいます',
      'Patch loaded for {path}': '{path} のパッチを読み込みました',
      'Patch unavailable: {error}': 'パッチを利用できません: {error}',
      'Provider usage': 'プロバイダー使用量',
      'Process result': 'プロセス結果',
      'Repository delta': 'リポジトリ差分',
      Verification: '検証',
      Unavailable: 'なし',
      Status: '状態',
      Session: 'セッション',
      Provider: 'プロバイダー',
      'Exit Reason': '終了理由',
      'Ended By': '終了要因',
      Duration: '所要時間',
      Warnings: '警告',
      Files: 'ファイル',
      'Stored Text': '保存済みテキスト',
      Baseline: 'ベースライン',
      Attribution: '出典',
      Window: '計測区間',
      Pinned: '固定',
      Reason: '理由',
      'Exit Code': '終了コード',
      Signal: 'シグナル',
      Check: 'チェック',
      Warning: '警告',
      Config: '設定',
      Version: 'バージョン',
      Scope: 'スコープ',
      Unparsed: '未解析',
      'Cost USD': 'コスト(USD)',
      'the provider\'s SessionEnd hook, as reported; agentrec did not observe the process end': 'プロバイダーの SessionEnd フック（報告どおり）。agentrec はプロセスの終了を観測していません',
      'the recorder, after no hook delivery for the idle timeout or on a signal; the session\'s own end was not seen': 'レコーダー（アイドルタイムアウトの間フック未到達、またはシグナル受信）。セッション自体の終了は観測されていません',
      'nothing yet: the session is still open and its recorder is running': 'まだなし: セッションは開いていて、レコーダーは実行中です',
      'baseline pinned at the SessionStart hook, not before the process started; measured after the session ended; the checkout was open to the operator in between': 'ベースラインはプロセス開始前ではなく SessionStart フック時点で固定し、セッション終了後に計測しました。その間チェックアウトはオペレーターが操作可能でした',
      'at the SessionStart hook; run after the session ended': 'SessionStart フック時点で固定、セッション終了後に実行',
      'Process outcome': 'プロセス結果',
      'Verification verdict': '検証の判定',
      'Repository evidence': 'リポジトリ証跡',
      'Normalized actions': '正規化されたアクション',
      'Run {run} · Verify {verify}': '実行 {run} · 検証 {verify}',
      'unknown cwd': '不明な作業ディレクトリ',
      '{provider} {version} is unsupported; this timeline may be incomplete.': '{provider} {version} は未対応のバージョンです。このタイムラインは不完全な可能性があります。',
      'unknown version': '不明なバージョン'
    },
    'zh-CN': {
      'Action Timeline': '操作时间线',
      'Loading recorded evidence…': '正在加载记录的证据…',
      'Recorded runs': '已记录的运行',
      'Find a run or project': '搜索运行或项目',
      Request: '请求',
      'No recorded request.': '没有记录的请求。',
      Actions: '操作',
      Changes: '变更',
      'Provider events': '提供方事件',
      'Filter timeline': '筛选时间线',
      'Evidence inspector': '证据检视器',
      'Select an action, change, or provider event to inspect its sanitized evidence.': '选择一个操作、变更或提供方事件即可查看其脱敏后的证据。',
      Language: '语言',
      'unknown project': '未知项目',
      unknown: '未知',
      'No runs recorded yet — start a Claude Code or Codex session; it appears here when it ends.': '尚未记录任何运行。启动 Claude Code 或 Codex 会话，结束后会显示在这里。',
      'No runs match this search.': '没有匹配的运行。',
      'No runs recorded yet': '尚未记录任何运行',
      'No run selected': '未选择运行',
      'Start a Claude Code or Codex session; it appears here when it ends.': '启动 Claude Code 或 Codex 会话，结束后会显示在这里。',
      'Pick a run from the list to inspect its recorded evidence.': '从列表中选择一个运行以查看其记录的证据。',
      'No recorded runs': '没有记录的运行',
      '{n} recorded run(s)': '已记录 {n} 个运行',
      'Could not load recorded runs': '无法加载记录的运行',
      'Could not load the latest run': '无法加载最新的运行',
      '{error} — retrying; pick a run from the list to try another.': '{error} — 正在重试；也可以从列表中选择其他运行。',
      '{n} unreadable run(s) were excluded.': '已排除 {n} 个无法读取的运行。',
      '{n}s ago': '{n}秒前',
      '{n}m ago': '{n}分钟前',
      '{n}h ago': '{n}小时前',
      '{n}d ago': '{n}天前',
      'No checks were run for this session. Record with --verify (agentrec setup --verify) to run the checks pinned in .agentrec.yaml.': '此会话未运行任何检查。使用 --verify 记录（agentrec setup --verify）即可运行 .agentrec.yaml 中固定的检查。',
      'agentrec did not launch this session, so exit code and signal were never seen.': 'agentrec 并未启动此会话，因此从未观测到退出码和信号。',
      'The end was reported by the provider\'s SessionEnd hook.': '结束由提供方的 SessionEnd 钩子报告。',
      'The diff is measured when the session ends.': 'diff 会在会话结束时测量。',
      'No repository diff was recorded for this run.': '此运行未记录仓库 diff。',
      'Observed during the run — this is not proof the agent caused it': '在运行期间观测到，但这并不能证明是代理造成的',
      'The repository diff artifacts are incomplete (git/result.json is missing).': '仓库 diff 产物不完整（缺少 git/result.json）。',
      'The repository diff artifacts are incomplete (the change list is missing).': '仓库 diff 产物不完整（缺少变更列表）。',
      'Reported by {provider}': '由 {provider} 报告',
      'Observed by agentrec': '由 agentrec 观测',
      'Observed by verification checks': '由验证检查观测',
      'The provider did not report usage for this run.': '提供方未报告此运行的用量。',
      'No process result was recorded.': '未记录进程结果。',
      'Session is still open; results appear when it ends.': '会话仍在进行中；结果会在结束后显示。',
      'The recorder ended without writing how the session ended.': '记录器已结束，但未写入会话的结束方式。',
      'Ended by the provider\'s SessionEnd hook, as reported.': '由提供方的 SessionEnd 钩子报告结束。',
      'No hook arrived for the idle timeout, or the recorder was signalled; the session\'s own end was not seen.': '在空闲超时内未收到钩子，或记录器收到了信号；未观测到会话本身的结束。',
      'The provider process exited normally.': '提供方进程正常退出。',
      'The provider process exited with a non-zero code.': '提供方进程以非零退出码结束。',
      'The run was interrupted before the provider finished.': '运行在提供方完成之前被中断。',
      'The run hit its time limit and the provider was stopped.': '运行达到时间上限，提供方已被停止。',
      'exit code {n}': '退出码 {n}',
      'signal {s}': '信号 {s}',
      'Duration unavailable': '无持续时间',
      '{passed} of {total} checks passed.': '{total} 项检查中有 {passed} 项通过。',
      '{passed} of {total} checks passed, {failed} failed.': '{total} 项检查中有 {passed} 项通过，{failed} 项失败。',
      'Every pinned check passed.': '所有固定的检查均已通过。',
      'At least one pinned check failed.': '至少一项固定的检查失败。',
      'A check was still running when its time limit ran out.': '有检查在时限内未完成。',
      'A check could not be run to completion.': '有检查未能运行完成。',
      'The pinned configuration changed under the run, so no checks were executed.': '固定的配置在运行期间发生了变化，因此未执行任何检查。',
      '{tracked} tracked · {untracked} untracked · +{additions}/−{deletions} · {binary} binary': '已跟踪 {tracked} · 未跟踪 {untracked} · +{additions}/−{deletions} · 二进制 {binary}',
      'No checks were run for this session.': '此会话未运行任何检查。',
      'Verification checks passed.': '验证检查已通过。',
      'Verification checks failed.': '验证检查失败。',
      'The run ended with {label}.': '运行以 {label} 结束。',
      'Previous page': '上一页',
      'Next page': '下一页',
      '{n} items on this page': '本页 {n} 项',
      'bounded patch page': '补丁分页（有大小上限）',
      'Could not load actions: {error}': '无法加载操作：{error}',
      'No loaded actions match this filter.': '没有符合筛选条件的操作。',
      'No actions were recorded for this run.': '此运行未记录任何操作。',
      'Loading actions…': '正在加载操作…',
      'Could not load repository changes: {error}': '无法加载仓库变更：{error}',
      'Repository change evidence is unavailable.': '仓库变更证据不可用。',
      'Loading repository changes…': '正在加载仓库变更…',
      'No repository changes were observed.': '未观测到仓库变更。',
      'No loaded changes match this filter.': '没有符合筛选条件的变更。',
      'Could not load provider events: {error}': '无法加载提供方事件：{error}',
      '(untyped)': '（无类型）',
      'event {n}': '事件 {n}',
      'provider event': '提供方事件',
      'No loaded provider events match this filter.': '没有符合筛选条件的提供方事件。',
      'This run has no sanitized provider-event artifact.': '此运行没有脱敏后的提供方事件产物。',
      'Loading provider events…': '正在加载提供方事件…',
      'same path observed — not causal proof': '观测到相同路径 — 并非因果证明',
      reported: '已报告',
      provider: '提供方',
      '{kind} selected: {label}': '已选择 {kind}：{label}',
      prompt: '提示',
      reply: '回复',
      You: '我',
      'Show more': '展开',
      'Show less': '收起',
      '… full text in the inspector': '… 全文见检视器',
      '(untyped event)': '（无类型事件）',
      'SAME PATH OBSERVED — NOT CAUSAL PROOF': '观测到相同路径 — 并非因果证明',
      'SANITIZED INPUT': '脱敏后的输入',
      'SANITIZED RESULT': '脱敏后的结果',
      'MESSAGE TEXT': '消息正文',
      tracked: '已跟踪',
      untracked: '未跟踪',
      binary: '二进制',
      'REPOSITORY-OBSERVED METADATA': '仓库观测元数据',
      'SANITIZED REPOSITORY PATCH': '脱敏后的仓库补丁',
      'Loading patch…': '正在加载补丁…',
      'No patch bytes on this page.': '本页没有补丁内容。',
      'event #{n}': '事件 #{n}',
      'SANITIZED PROVIDER EVENT': '脱敏后的提供方事件',
      'Loading patch for {path}': '正在加载 {path} 的补丁',
      'Patch loaded for {path}': '已加载 {path} 的补丁',
      'Patch unavailable: {error}': '补丁不可用：{error}',
      'Provider usage': '提供方用量',
      'Process result': '进程结果',
      'Repository delta': '仓库差异',
      Verification: '验证',
      Unavailable: '不可用',
      Status: '状态',
      Session: '会话',
      Provider: '提供方',
      'Exit Reason': '退出原因',
      'Ended By': '结束方式',
      Duration: '持续时间',
      Warnings: '警告',
      Files: '文件',
      'Stored Text': '已存储文本',
      Baseline: '基线',
      Attribution: '归属',
      Window: '测量窗口',
      Pinned: '固定',
      Reason: '原因',
      'Exit Code': '退出码',
      Signal: '信号',
      Check: '检查',
      Warning: '警告',
      Config: '配置',
      Version: '版本',
      Scope: '范围',
      Unparsed: '未解析',
      'Cost USD': '费用(USD)',
      'the provider\'s SessionEnd hook, as reported; agentrec did not observe the process end': '提供方的 SessionEnd 钩子（按其报告）；agentrec 未观测到进程结束',
      'the recorder, after no hook delivery for the idle timeout or on a signal; the session\'s own end was not seen': '记录器（空闲超时内未收到钩子，或收到信号）；未观测到会话本身的结束',
      'nothing yet: the session is still open and its recorder is running': '暂无：会话仍在进行，记录器正在运行',
      'baseline pinned at the SessionStart hook, not before the process started; measured after the session ended; the checkout was open to the operator in between': '基线在 SessionStart 钩子时固定（而非进程启动前），并在会话结束后测量；期间检出目录对操作者开放',
      'at the SessionStart hook; run after the session ended': '在 SessionStart 钩子时固定；会话结束后运行',
      'Process outcome': '进程结果',
      'Verification verdict': '验证结论',
      'Repository evidence': '仓库证据',
      'Normalized actions': '规范化操作',
      'Run {run} · Verify {verify}': '运行 {run} · 验证 {verify}',
      'unknown cwd': '未知工作目录',
      '{provider} {version} is unsupported; this timeline may be incomplete.': '不支持 {provider} {version}；此时间线可能不完整。',
      'unknown version': '未知版本'
    }
  };

  // t looks up page-authored copy by its English key; English and missing entries fall back to the key itself.
  function t(key, vars) {
    const table = STRINGS[state.lang];
    let text = (table && table[key]) || key;
    if (vars) for (const [name, value] of Object.entries(vars)) text = text.split(`{${name}}`).join(String(value));
    return text;
  }

  function detectLang() {
    try {
      const saved = localStorage.getItem('agentrec.lang');
      if (LANGS.includes(saved)) return saved;
    } catch (error) {
      // storage unavailable; fall through to the browser preference
    }
    for (const tag of navigator.languages || [navigator.language || 'en']) {
      const lower = String(tag).toLowerCase();
      if (lower.startsWith('ko')) return 'ko';
      if (lower.startsWith('ja')) return 'ja';
      if (lower.startsWith('zh')) return 'zh-CN';
      if (lower.startsWith('en')) return 'en';
    }
    return 'en';
  }

  function localizeStatic() {
    document.querySelectorAll('[data-i18n]').forEach((el) => { el.textContent = t(el.dataset.i18n); });
    document.querySelectorAll('[data-i18n-placeholder]').forEach((el) => {
      el.placeholder = t(el.dataset.i18nPlaceholder);
      el.setAttribute('aria-label', el.placeholder);
    });
    $('lang').setAttribute('aria-label', t('Language'));
  }

  function setLang(lang) {
    state.lang = LANGS.includes(lang) ? lang : 'en';
    document.documentElement.lang = state.lang;
    try {
      localStorage.setItem('agentrec.lang', state.lang);
    } catch (error) {
      // storage unavailable; the choice lasts for this page only
    }
    $('lang').value = state.lang;
    localizeStatic();
  }

  async function getJSON(path, signal) {
    const response = await fetch(path, { headers: { Accept: 'application/json' }, signal });
    const body = await response.json().catch(() => ({ error: `HTTP ${response.status}` }));
    if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
    return body;
  }

  function showError(error) {
    const toast = $('error');
    toast.textContent = error instanceof Error ? error.message : String(error);
    toast.classList.remove('hidden');
    window.setTimeout(() => toast.classList.add('hidden'), 7000);
  }

  function announceInspector(message) {
    $('inspector-status').textContent = message;
  }

  function shortID(id) {
    return id.length > 30 ? `${id.slice(0, 21)}…${id.slice(-6)}` : id;
  }

  function relativeTime(value) {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return t('unknown');
    const seconds = Math.round((Date.now() - date.getTime()) / 1000);
    if (seconds < 60) return t('{n}s ago', { n: Math.max(0, seconds) });
    if (seconds < 3600) return t('{n}m ago', { n: Math.round(seconds / 60) });
    if (seconds < 86400) return t('{n}h ago', { n: Math.round(seconds / 3600) });
    return t('{n}d ago', { n: Math.round(seconds / 86400) });
  }

  function clock(value) {
    if (!value || value.startsWith('0001-')) return '—';
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return '—';
    return date.toLocaleTimeString(state.lang, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
  }

  function duration(action) {
    if (!action.startedAt || !action.finishedAt || action.startedAt.startsWith('0001-') || action.finishedAt.startsWith('0001-')) return '';
    const ms = new Date(action.finishedAt) - new Date(action.startedAt);
    if (!Number.isFinite(ms) || ms < 0) return '';
    return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(ms < 10000 ? 2 : 1)}s`;
  }

  function statusClass(value) {
    const v = String(value || '').toLowerCase();
    if (v === 'pass' || v === 'passed' || v === 'completed' || v === 'success') return 'pass';
    if (['fail', 'failed', 'error', 'timeout', 'nonzero', 'interrupted', 'parse_error', 'storage_error', 'start_error'].includes(v)) return 'fail';
    return '';
  }

  // Plain-English copy for the status vocabulary (English keys; t() localizes at render time).
  // The raw tokens stay (they are documented and pinned); these only explain them.
  const NO_VERIFICATION = 'No checks were run for this session. Record with --verify (agentrec setup --verify) to run the checks pinned in .agentrec.yaml.';
  const NOT_OBSERVED = 'agentrec did not launch this session, so exit code and signal were never seen.';
  const ENDED_BY_HOOK = 'The end was reported by the provider\'s SessionEnd hook.';
  const REPOSITORY_PENDING = 'The diff is measured when the session ends.';
  const REPOSITORY_NONE = 'No repository diff was recorded for this run.';
  const NOT_CAUSAL = 'Observed during the run — this is not proof the agent caused it';
  const REPOSITORY_REASONS = {
    'repository change evidence was not recorded': REPOSITORY_NONE,
    'repository change evidence is pending': REPOSITORY_PENDING,
    'repository change artifacts are missing git/result.json': 'The repository diff artifacts are incomplete (git/result.json is missing).',
    'repository result is missing change-list evidence': 'The repository diff artifacts are incomplete (the change list is missing).'
  };
  const ATTRIBUTIONS = {
    provider_reported: (provider) => t('Reported by {provider}', { provider: provider || t('provider') }),
    supervisor_observed: () => t('Observed by agentrec'),
    verification_observed: () => t('Observed by verification checks'),
    'observed during run, not causal proof': () => t(NOT_CAUSAL),
    'observed, not causal proof': () => t(NOT_CAUSAL)
  };
  const EMPTY_SECTION = {
    'Provider usage': 'The provider did not report usage for this run.',
    'Process result': 'No process result was recorded.',
    'Repository delta': REPOSITORY_NONE,
    Verification: NO_VERIFICATION
  };
  const OUTCOMES = {
    running: { value: 'RUNNING', tone: '', detail: 'Session is still open; results appear when it ends.' },
    unknown: { value: 'UNKNOWN', tone: '', detail: 'The recorder ended without writing how the session ended.' },
    session_ended: { value: 'ENDED', tone: '', detail: 'Ended by the provider\'s SessionEnd hook, as reported.' },
    session_lost: { value: 'LOST', tone: 'warn', detail: 'No hook arrived for the idle timeout, or the recorder was signalled; the session\'s own end was not seen.' },
    completed: { detail: 'The provider process exited normally.' },
    nonzero: { detail: 'The provider process exited with a non-zero code.' },
    interrupted: { detail: 'The run was interrupted before the provider finished.' },
    timeout: { detail: 'The run hit its time limit and the provider was stopped.' }
  };
  const VERDICTS = {
    PASS: 'Every pinned check passed.',
    FAIL: 'At least one pinned check failed.',
    TIMEOUT: 'A check was still running when its time limit ran out.',
    ERROR: 'A check could not be run to completion.',
    TAINTED: 'The pinned configuration changed under the run, so no checks were executed.',
    UNAVAILABLE: NO_VERIFICATION
  };
  const TYPE_LABELS = { 'user.prompt': 'prompt', 'agent.message': 'reply' };

  function humanAttribution(raw, provider) {
    const known = ATTRIBUTIONS[raw];
    return known ? known(provider) : raw;
  }

  // labelled renders humanised text and keeps the raw token reachable in the title attribute.
  function labelled(tag, className, text, raw) {
    const el = node(tag, className, text);
    if (raw && raw !== text) el.title = raw;
    return el;
  }

  function sentence(text) {
    const value = String(text || '').trim();
    if (!value) return '';
    if (REPOSITORY_REASONS[value]) return t(REPOSITORY_REASONS[value]);
    return value[0].toUpperCase() + value.slice(1) + (/[.!?]$/.test(value) ? '' : '.');
  }

  function outcome(exitReason, supervisor) {
    const reason = String(exitReason || 'running').toLowerCase();
    const known = OUTCOMES[reason] || {};
    if (known.value) return { value: known.value, tone: known.tone, detail: t(known.detail) };
    const facts = [supervisor.has('Exit Code') ? t('exit code {n}', { n: supervisor.get('Exit Code') }) : '', supervisor.get('Signal') ? t('signal {s}', { s: supervisor.get('Signal') }) : '', supervisor.get('Duration') || ''];
    if (known.detail) facts.unshift(t(known.detail));
    return { value: reason.toUpperCase(), tone: statusClass(reason), detail: facts.filter(Boolean).join(' · ') || t('Duration unavailable') };
  }

  function verificationSummary(fields) {
    if (!fields || fields.length === 0) return { value: 'UNAVAILABLE', tone: '', detail: t(NO_VERIFICATION) };
    const map = fieldsMap(fields);
    const status = String(map.get('Status') || 'UNAVAILABLE').toUpperCase();
    const checks = fields.filter((field) => field.name === 'Check');
    const passed = checks.filter((field) => String(field.value).startsWith('PASS ')).length;
    const failed = checks.filter((field) => String(field.value).startsWith('FAIL ')).length;
    let detail = sentence(map.get('Reason'));
    if (checks.length) detail = t(failed ? '{passed} of {total} checks passed, {failed} failed.' : '{passed} of {total} checks passed.', { passed, failed, total: checks.length });
    else if (!detail && VERDICTS[status]) detail = t(VERDICTS[status]);
    return { value: status, tone: statusClass(status), detail };
  }

  function repositorySummary(changes, fields) {
    const status = String(changes.status || 'unavailable').toUpperCase();
    if (status === 'AVAILABLE') {
      return { value: String(changes.total || 0), detail: t('{tracked} tracked · {untracked} untracked · +{additions}/−{deletions} · {binary} binary', { tracked: changes.tracked || 0, untracked: changes.untracked || 0, additions: changes.additions || 0, deletions: changes.deletions || 0, binary: changes.binary || 0 }) };
    }
    if (status === 'PENDING' || fields.get('Status') === 'PENDING') return { value: 'PENDING', detail: t(REPOSITORY_PENDING) };
    return { value: 'UNAVAILABLE', detail: sentence(changes.reason) || t(REPOSITORY_NONE) };
  }

  // explainStatus is the hover text for the server-classified run-list chip.
  function explainStatus(label) {
    const v = String(label || '').toUpperCase();
    if (v === 'UNAVAILABLE') return t('No checks were run for this session.');
    if (v === 'PASS') return t('Verification checks passed.');
    if (v === 'FAIL') return t('Verification checks failed.');
    const known = OUTCOMES[String(label || '').toLowerCase()];
    return known && known.value ? t(known.detail) : t('The run ended with {label}.', { label });
  }

  function renderRunList() {
    const query = $('run-search').value.trim().toLowerCase();
    const list = $('run-list');
    const focused = document.activeElement && document.activeElement.dataset ? document.activeElement.dataset.runId : undefined;
    list.replaceChildren();
    let shown = 0;
    for (const run of state.runs) {
      const haystack = `${run.id} ${run.provider} ${run.project} ${run.exit} ${run.verification}`.toLowerCase();
      if (query && !haystack.includes(query)) continue;
      shown += 1;
      const active = Boolean(state.run && state.run.run.id === run.id);
      const button = node('button', `run-item${active ? ' active' : ''}`);
      button.type = 'button';
      button.dataset.runId = run.id;
      if (active) button.setAttribute('aria-current', 'true');
      const head = node('div', 'run-item-head');
      head.append(node('span', 'run-project', run.project || t('unknown project')), node('span', 'run-time', relativeTime(run.startedAt)));
      const foot = node('div', 'run-item-foot');
      const status = node('span', `mini-status ${run.statusClass}`, run.statusLabel);
      status.title = explainStatus(run.statusLabel);
      foot.append(node('span', 'run-provider', run.provider || t('unknown')), status);
      button.append(head, node('div', 'run-id', shortID(run.id)), foot);
      button.addEventListener('click', () => loadRun(run.id));
      list.append(button);
      if (focused === run.id) button.focus({ preventScroll: true });
    }
    $('run-count').textContent = String(state.runs.length);
    const empty = $('run-list-empty');
    if (shown === 0) {
      empty.textContent = t(state.runs.length === 0 ? 'No runs recorded yet — start a Claude Code or Codex session; it appears here when it ends.' : 'No runs match this search.');
      empty.classList.remove('hidden');
    } else {
      empty.classList.add('hidden');
    }
  }

  function renderWorkspaceState() {
    const empty = $('workspace-empty');
    const view = $('run-view');
    if (state.run) {
      empty.classList.add('hidden');
      view.classList.remove('hidden');
      return;
    }
    view.classList.add('hidden');
    empty.classList.remove('hidden');
    const noRuns = state.runs.length === 0;
    $('workspace-empty-title').textContent = t(noRuns ? 'No runs recorded yet' : 'No run selected');
    $('workspace-empty-body').textContent = t(noRuns ? 'Start a Claude Code or Codex session; it appears here when it ends.' : 'Pick a run from the list to inspect its recorded evidence.');
    $('top-meta').textContent = noRuns ? t('No recorded runs') : t('{n} recorded run(s)', { n: state.runs.length });
  }

  function firstDetail(value) {
    if (!value || typeof value !== 'object') return '';
    const keys = ['query', 'pattern', 'command', 'cmd', 'file_path', 'filePath', 'path', 'url', 'prompt', 'description', 'name', 'text'];
    for (const key of keys) {
      if (typeof value[key] === 'string' && value[key]) return value[key].replace(/\s+/g, ' ').slice(0, 180);
    }
    if (Array.isArray(value.command)) return value.command.join(' ').slice(0, 180);
    return '';
  }

  function actionFamily(type) {
    const value = String(type || 'unknown');
    if (value === 'user.prompt') return 'prompt';
    if (value.includes('search')) return 'search';
    if (value.includes('read')) return 'read';
    if (value.includes('write')) return 'write';
    if (value.includes('edit') || value.includes('patch')) return 'edit';
    if (value.includes('shell') || value.includes('exec') || value.includes('command')) return 'exec';
    if (value.includes('mcp')) return 'mcp';
    if (value.includes('subagent')) return 'agent';
    if (value.includes('message')) return 'message';
    if (value.includes('error')) return 'error';
    return 'tool';
  }

  // conversationText returns the operator's prompt or the assistant's reply for the two hook-recorded conversation actions, else null.
  function conversationText(action) {
    const input = action.input || {};
    if (action.type === 'user.prompt') return typeof input.prompt === 'string' ? input.prompt : '';
    if (action.type === 'agent.message') return typeof input.text === 'string' ? input.text : '';
    return null;
  }

  function changeFamily(change) {
    if (!change.tracked) return 'untracked';
    if (change.binary) return 'binary';
    return 'tracked';
  }

  function actionDepth(action, byID) {
    let depth = 0;
    let parent = action.parentId;
    const seen = new Set();
    while (parent && byID.has(parent) && !seen.has(parent) && depth < 5) {
      seen.add(parent);
      depth += 1;
      parent = byID.get(parent).parentId;
    }
    return depth;
  }

  function renderTypeFilters(items, typeOf) {
    const holder = $('type-filters');
    holder.replaceChildren();
    const counts = new Map();
    for (const item of items) {
      const type = typeOf(item);
      counts.set(type, (counts.get(type) || 0) + 1);
    }
    for (const [type, count] of [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
      const chip = node('button', `filter-chip${state.activeTypes.size === 0 || state.activeTypes.has(type) ? ' active' : ''}`, `${t(TYPE_LABELS[type] || type)} ${count}`);
      chip.type = 'button';
      chip.setAttribute('aria-pressed', String(state.activeTypes.size === 0 || state.activeTypes.has(type)));
      chip.addEventListener('click', () => {
        if (state.activeTypes.size === 0) for (const key of counts.keys()) state.activeTypes.add(key);
        if (state.activeTypes.has(type)) state.activeTypes.delete(type); else state.activeTypes.add(type);
        if (state.activeTypes.size === counts.size) state.activeTypes.clear();
        renderTimeline();
      });
      holder.append(chip);
    }
  }

  function matches(item, type, text) {
    if (state.activeTypes.size > 0 && !state.activeTypes.has(type)) return false;
    if (!state.query) return true;
    return text.toLowerCase().includes(state.query);
  }

  function appendPager(timeline, streamName) {
    const stream = state.streams[streamName];
    if (!stream || (stream.history.length === 0 && stream.nextCursor === null)) return;
    const controls = node('div', 'stream-pager');
    const previous = node('button', 'load-more', t('Previous page'));
    previous.type = 'button';
    previous.disabled = stream.loading || stream.history.length === 0;
    previous.addEventListener('click', () => {
      const cursor = stream.history[stream.history.length - 1];
      loadStreamPage(streamName, cursor, false, state.loadGeneration, true);
    });
    const next = node('button', 'load-more', t('Next page'));
    next.type = 'button';
    next.disabled = stream.loading || stream.nextCursor === null;
    next.addEventListener('click', () => loadStreamPage(streamName, stream.nextCursor, true));
    controls.append(previous, node('span', 'pager-label', t('{n} items on this page', { n: stream.items.length })), next);
    timeline.append(controls);
  }

  // One focusable, keyboard-activatable timeline row; shared by actions, changes and events.
  function timelineRow(className, select) {
    const row = node('article', className);
    row.tabIndex = 0;
    row.setAttribute('role', 'button');
    row.setAttribute('aria-controls', 'inspector');
    row.addEventListener('click', select);
    row.addEventListener('keydown', (event) => {
      if (event.target !== row) return;
      if (event.key === 'Enter' || event.key === ' ') {
        if (event.key === ' ') event.preventDefault();
        select();
      }
    });
    return row;
  }

  // speechBlock shows a capped two-line preview that expands in place up to a cap; the inspector carries the full text.
  const PREVIEW_CHARS = 280;
  const EXPANDED_CHARS = 20000;
  function speechBlock(text) {
    const block = node('div', 'speech');
    const preview = text.split('\n').slice(0, 2).join('\n').slice(0, PREVIEW_CHARS);
    const quote = node('div', 'speech-text', preview);
    block.append(quote);
    if (preview.length === text.length) return block;
    const more = node('button', 'show-more', t('Show more'));
    more.type = 'button';
    let expanded = false;
    more.addEventListener('click', (event) => {
      event.stopPropagation();
      expanded = !expanded;
      quote.textContent = expanded ? (text.length > EXPANDED_CHARS ? `${text.slice(0, EXPANDED_CHARS)}\n${t('… full text in the inspector')}` : text) : preview;
      quote.classList.toggle('expanded', expanded);
      more.textContent = t(expanded ? 'Show less' : 'Show more');
    });
    block.append(more);
    return block;
  }

  function renderTimeline() {
    if (!state.run) return;
    const timeline = $('timeline');
    timeline.replaceChildren();
    state.selected = null;
    renderInspector();

    if (state.mode === 'actions') {
      const stream = state.streams.actions;
      const actions = stream.items;
      if (stream.error) timeline.append(node('div', 'timeline-empty stream-error', t('Could not load actions: {error}', { error: stream.error })));
      renderTypeFilters(actions, (item) => item.type || 'unknown');
      const byID = new Map(actions.map((action) => [action.id, action]));
      let shown = 0;
      actions.forEach((action, index) => {
        const type = action.type || 'unknown';
        const speech = conversationText(action);
        const detail = speech === null ? (firstDetail(action.input) || firstDetail(action.result)) : speech.slice(0, 180);
        const searchable = `${type} ${action.provider || ''} ${action.status || ''} ${detail} ${JSON.stringify(action.input || {})}`;
        if (!matches(action, type, searchable)) return;
        shown += 1;
        const family = actionFamily(type);
        const row = timelineRow(`action-row${speech === null ? '' : ` conversation-row ${family}`}`, () => selectItem(row, { kind: 'action', value: action }));
        row.style.setProperty('--depth', String(actionDepth(action, byID)));
        row.dataset.index = String(index);
        const time = node('div', 'action-time', clock(action.startedAt));
        if (speech !== null) {
          const body = node('div', 'speech-body');
          body.append(node('div', 'speaker', type === 'user.prompt' ? t('You') : (action.provider || t('provider'))), speechBlock(speech));
          row.append(time, body);
          timeline.append(row);
          return;
        }
        const rail = node('div', 'action-rail');
        rail.append(node('span', `action-dot ${family} ${statusClass(action.status)}`));
        const body = node('div', 'action-body');
        const head = node('div', 'action-head');
        head.append(node('span', 'action-type', type), node('span', 'action-summary', detail || action.id));
        const meta = node('div', 'action-meta');
        meta.append(node('span', 'source-badge', action.provider || t('provider')), node('span', '', action.status || t('reported')));
        const elapsed = duration(action);
        if (elapsed) meta.append(node('span', '', elapsed));
        if (action.parentId) meta.append(node('span', '', `↳ ${shortID(action.parentId)}`));
        const observedPaths = action.samePathObserved || [];
        if (observedPaths.length) meta.append(node('span', 'path-correlation', t('same path observed — not causal proof')));
        body.append(head, meta);
        row.append(time, rail, body);
        timeline.append(row);
      });
      if (shown === 0 && !stream.error) {
        const message = stream.loaded ? (actions.length ? 'No loaded actions match this filter.' : 'No actions were recorded for this run.') : 'Loading actions…';
        timeline.append(node('div', 'timeline-empty', t(message)));
      }
      appendPager(timeline, 'actions');
      return;
    }

    if (state.mode === 'changes') {
      const stream = state.streams.changes;
      const changes = stream.items;
      const source = stream.attribution || 'observed during run, not causal proof';
      if (stream.error) timeline.append(node('div', 'timeline-empty stream-error', t('Could not load repository changes: {error}', { error: stream.error })));
      renderTypeFilters(changes, changeFamily);
      if (stream.loaded && stream.status === 'unavailable') {
        const pending = fieldsMap(state.run.evidence.repository).get('Status') === 'PENDING';
        const reason = pending ? t(REPOSITORY_PENDING) : sentence(stream.reason);
        timeline.append(node('div', 'timeline-empty change-evidence-warning', `${t('Repository change evidence is unavailable.')}${reason ? ` ${reason}` : ''}`));
      }
      if (changes.length) timeline.append(labelled('div', 'timeline-note', humanAttribution(source), source));
      let shown = 0;
      changes.forEach((change) => {
        const type = changeFamily(change);
        const counts = change.binary ? t('binary') : [change.additions === undefined ? '' : `+${change.additions}`, change.deletions === undefined ? '' : `-${change.deletions}`].filter(Boolean).join(' ');
        if (!matches(change, type, `${type} ${change.path} ${change.kind || ''} ${counts}`)) return;
        shown += 1;
        const row = timelineRow('action-row change-row', () => {
          selectItem(row, { kind: 'change', value: change, patch: null, patchCursor: 0, patchNextCursor: null, patchHistory: [], patchLoading: false });
          if (change.tracked) loadPatchPage(change.path, 0, false, state.loadGeneration);
        });
        const marker = node('div', `change-marker ${type}`, change.tracked ? (change.binary ? 'B' : 'M') : '?');
        const rail = node('div', 'action-rail');
        rail.append(node('span', `action-dot ${type}`));
        const body = node('div', 'action-body');
        const head = node('div', 'action-head');
        head.append(node('span', 'action-type', change.path));
        const meta = node('div', 'action-meta');
        meta.append(node('span', '', t(type)));
        if (counts) meta.append(node('span', 'change-counts', counts));
        body.append(head, meta);
        row.append(marker, rail, body);
        timeline.append(row);
      });
      if (shown === 0 && !stream.error && !(stream.loaded && stream.status === 'unavailable')) {
        let message = 'Loading repository changes…';
        if (stream.loaded && stream.total === 0) message = 'No repository changes were observed.';
        else if (stream.loaded) message = 'No loaded changes match this filter.';
        timeline.append(node('div', 'timeline-empty', t(message)));
      }
      appendPager(timeline, 'changes');
      return;
    }

    const stream = state.streams.events;
    const events = stream.items;
    if (stream.error) timeline.append(node('div', 'timeline-empty stream-error', t('Could not load provider events: {error}', { error: stream.error })));
    const eventType = (event) => typeof event.type === 'string' && event.type ? event.type : '(untyped)';
    renderTypeFilters(events, eventType);
    let shown = 0;
    events.forEach((event, index) => {
      const type = eventType(event);
      const detail = firstDetail(event) || firstDetail(event.message) || event.subtype || event.event || t('event {n}', { n: index + 1 });
      if (!matches(event, type, `${type} ${detail} ${JSON.stringify(event)}`)) return;
      shown += 1;
      const row = timelineRow('action-row event-row', () => selectItem(row, { kind: 'event', value: event, index }));
      const time = node('div', 'action-time', clock(event.timestamp || event.created_at || event.createdAt));
      const rail = node('div', 'action-rail');
      rail.append(node('span', 'action-dot'));
      const body = node('div', 'action-body');
      const head = node('div', 'action-head');
      head.append(node('span', 'action-type', t(type)), node('span', 'action-summary', detail));
      const meta = node('div', 'action-meta');
      meta.append(node('span', 'source-badge', t('provider event')), node('span', '', `#${index + 1}`));
      body.append(head, meta);
      row.append(time, rail, body);
      timeline.append(row);
    });
    if (shown === 0 && !stream.error) {
      const message = stream.loaded ? (events.length ? 'No loaded provider events match this filter.' : 'This run has no sanitized provider-event artifact.') : 'Loading provider events…';
      timeline.append(node('div', 'timeline-empty', t(message)));
    }
    appendPager(timeline, 'events');
  }

  function selectItem(row, selected) {
    document.querySelectorAll('.action-row.selected').forEach((el) => {
      el.classList.remove('selected');
    });
    row.classList.add('selected');
    state.selected = selected;
    renderInspector();
    const label = selected.kind === 'change' ? selected.value.path : (selected.value.type || selected.kind);
    announceInspector(t('{kind} selected: {label}', { kind: selected.kind, label }));
  }

  function addPayload(holder, label, value) {
    if (value === undefined || value === null || value === '') return;
    holder.append(node('div', 'payload-label', t(label)));
    const text = typeof value === 'string' ? value : JSON.stringify(value, null, 2);
    holder.append(node('pre', 'payload', text));
  }

  // Unified-diff lines rendered as text nodes with a tone class; no markup is derived from the payload.
  function patchNode(patch) {
    const pre = node('pre', 'payload diff-patch');
    for (const line of patch.split('\n')) {
      let tone = '';
      if (line.startsWith('+++') || line.startsWith('---') || line.startsWith('diff ') || line.startsWith('index ')) tone = ' diff-meta';
      else if (line.startsWith('+')) tone = ' diff-add';
      else if (line.startsWith('-')) tone = ' diff-del';
      else if (line.startsWith('@@')) tone = ' diff-hunk';
      pre.append(node('span', `diff-line${tone}`, `${line}\n`));
    }
    return pre;
  }

  function renderInspector() {
    const holder = $('inspector');
    holder.replaceChildren();
    if (!state.selected) {
      holder.className = 'inspector-empty';
      holder.textContent = t('Select an action, change, or provider event to inspect its sanitized evidence.');
      return;
    }
    holder.className = '';
    const { kind, value } = state.selected;
    const title = kind === 'action' ? value.type : (kind === 'change' ? value.path : (value.type || t('(untyped event)')));
    holder.append(node('div', 'inspector-title', title));
    const meta = node('div', 'inspector-meta');
    if (kind === 'action') {
      if (value.provider) meta.append(node('span', 'pill', value.provider));
      if (value.assurance) meta.append(labelled('span', 'pill', humanAttribution(value.assurance, value.provider), value.assurance));
      [value.status, value.id].filter(Boolean).forEach((item) => meta.append(node('span', 'pill', item)));
      const observedPaths = value.samePathObserved || [];
      if (observedPaths.length) meta.append(node('span', 'pill path-correlation', t('same path observed — not causal proof')));
      holder.append(meta);
      if (observedPaths.length) addPayload(holder, 'SAME PATH OBSERVED — NOT CAUSAL PROOF', observedPaths);
      const speech = conversationText(value);
      if (speech === null) addPayload(holder, 'SANITIZED INPUT', value.input);
      else addPayload(holder, 'MESSAGE TEXT', speech);
      addPayload(holder, 'SANITIZED RESULT', value.result);
    } else if (kind === 'change') {
      const source = state.streams.changes.attribution || 'observed during run, not causal proof';
      meta.append(node('span', 'pill', t(value.tracked ? 'tracked' : 'untracked')), labelled('span', 'pill', humanAttribution(source), source));
      if (value.binary) meta.append(node('span', 'pill', t('binary')));
      holder.append(meta);
      const details = {
        kind: value.kind,
        additions: value.additions,
        deletions: value.deletions,
        mode: value.mode,
        size: value.size,
        stored: value.stored,
        reason: value.reason
      };
      Object.keys(details).forEach((key) => details[key] === undefined && delete details[key]);
      addPayload(holder, 'REPOSITORY-OBSERVED METADATA', details);
      if (value.tracked) {
        holder.append(node('div', 'payload-label', t('SANITIZED REPOSITORY PATCH')));
        const { patchLoading, patchError, patch } = state.selected;
        if (!patchLoading && !patchError && patch) {
          holder.append(patchNode(patch));
        } else {
          holder.append(node('pre', 'payload diff-patch', patchLoading ? t('Loading patch…') : (patchError || t('No patch bytes on this page.'))));
        }
        if (!state.selected.patchLoading && (state.selected.patchHistory.length > 0 || state.selected.patchNextCursor !== null)) {
          const controls = node('div', 'stream-pager patch-pager');
          const previous = node('button', 'load-more', t('Previous page'));
          previous.type = 'button';
          previous.disabled = state.selected.patchHistory.length === 0;
          previous.addEventListener('click', () => {
            const cursor = state.selected.patchHistory[state.selected.patchHistory.length - 1];
            loadPatchPage(value.path, cursor, false, state.loadGeneration, true);
          });
          const next = node('button', 'load-more', t('Next page'));
          next.type = 'button';
          next.disabled = state.selected.patchNextCursor === null;
          next.addEventListener('click', () => loadPatchPage(value.path, state.selected.patchNextCursor, true, state.loadGeneration));
          controls.append(previous, node('span', 'pager-label', t('bounded patch page')), next);
          holder.append(controls);
        }
      }
    } else {
      meta.append(labelled('span', 'pill', humanAttribution('provider_reported', state.run.run.provider), 'provider_reported'), node('span', 'pill', t('event #{n}', { n: state.selected.index + 1 })));
      holder.append(meta);
      addPayload(holder, 'SANITIZED PROVIDER EVENT', value);
    }
  }

  function fieldsMap(fields) {
    return new Map((fields || []).map((field) => [field.name, field.value]));
  }

  // describeField turns a raw evidence value into display text plus, where the word alone is opaque, a one-line caption.
  // Server sentences are translated only by exact match through t(); anything else is shown verbatim.
  function describeField(section, name, value, fields, provider) {
    if (name === 'Attribution') return { text: humanAttribution(value, provider), title: value };
    if (section === 'Process result' && name === 'Status' && value.startsWith('UNAVAILABLE (interactive session')) {
      const ended = fields.get('Exit Reason') === 'session_ended';
      return { text: 'NOT OBSERVED', caption: ended ? `${t(NOT_OBSERVED)} ${t(ENDED_BY_HOOK)}` : t(NOT_OBSERVED), title: value };
    }
    if (section === 'Repository delta' && name === 'Status') {
      if (value === 'PENDING') return { text: value, caption: t(REPOSITORY_PENDING) };
      if (value === 'UNAVAILABLE') return { text: value, caption: sentence(fields.get('Reason')) || t(REPOSITORY_NONE) };
    }
    if (section === 'Verification' && name === 'Status' && VERDICTS[value]) return { text: value, caption: sentence(fields.get('Reason')) || t(VERDICTS[value]) };
    return { text: t(value), title: value };
  }

  function fieldValue(described) {
    const el = node('span', 'field-value', described.text);
    if (described.title && described.title !== described.text) el.title = described.title;
    if (described.caption) el.append(node('span', 'field-caption', described.caption));
    return el;
  }

  function renderEvidence() {
    const holder = $('evidence-sections');
    holder.replaceChildren();
    const provider = state.run.run.provider;
    const sections = [
      ['Provider usage', 'provider_reported', state.run.evidence.providerUsage || []],
      ['Process result', 'supervisor_observed', state.run.evidence.supervisor || []],
      ['Repository delta', 'observed, not causal proof', state.run.evidence.repository || []],
      ['Verification', 'verification_observed', state.run.evidence.verification || []]
    ];
    for (const [title, source, fields] of sections) {
      const block = node('section', 'evidence-block');
      const heading = node('div', 'evidence-title');
      heading.append(node('span', '', t(title)), labelled('span', 'evidence-source', humanAttribution(source, provider), source));
      block.append(heading);
      const grid = node('div', 'evidence-fields');
      if (fields.length === 0) {
        const empty = fieldValue({ text: t('Unavailable'), caption: t(EMPTY_SECTION[title]) });
        empty.classList.add('field-span');
        grid.append(empty);
      } else {
        const map = fieldsMap(fields);
        for (const field of fields) grid.append(labelled('span', 'field-name', t(field.name), field.name), fieldValue(describeField(title, field.name, String(field.value), map, provider)));
      }
      block.append(grid);
      holder.append(block);
    }
  }

  // tone is one of '', 'pass', 'fail', 'warn'; UNAVAILABLE and RUNNING stay neutral on purpose.
  function metric(label, value, detail = '', tone = '') {
    const card = node('div', `metric${tone ? ` ${tone}` : ''}`);
    card.append(node('div', 'metric-label', t(label)), node('div', 'metric-value', String(value)));
    if (detail) card.append(node('div', 'metric-detail', detail));
    return card;
  }

  function renderRun() {
    const data = state.run;
    const run = data.run;
    $('run-provider').textContent = run.provider || t('unknown');
    $('run-project').textContent = run.project || t('unknown');
    $('run-title').textContent = run.id;
    $('run-subtitle').textContent = `${run.cwd || t('unknown cwd')} · ${new Date(run.startedAt).toLocaleString(state.lang)}`;
    $('run-prompt').textContent = run.prompt || t('No recorded request.');
    $('action-count').textContent = String(data.actionCount || 0);
    $('change-count').textContent = '0';
    $('event-count').textContent = String(data.eventCount || 0);
    $('top-meta').textContent = `${run.provider || t('unknown')} · ${run.exitReason || 'running'} · ${run.id}`;
    document.title = `${run.project || run.id} · agentrec`;

    const supervisor = fieldsMap(data.evidence.supervisor);
    const process = outcome(run.exitReason, supervisor);
    const verification = verificationSummary(data.evidence.verification);
    const tones = [process.tone, verification.tone];
    // ponytail: a run reads as passed only when both the process and the checks did; UNAVAILABLE never borrows green.
    const runStatus = tones.includes('fail') ? 'fail' : (tones.includes('warn') ? 'warn' : (process.tone === 'pass' && verification.tone === 'pass' ? 'pass' : ''));
    const verdict = $('run-verdict');
    verdict.textContent = t('Run {run} · Verify {verify}', { run: process.value, verify: verification.value });
    verdict.title = `${process.detail} ${verification.detail}`.trim();
    verdict.className = `verdict ${runStatus}`;
    $('provider-dot').className = `status-dot ${runStatus}`;
    const timelineWarning = $('timeline-warning');
    if (run.versionUnverified) {
      timelineWarning.textContent = t('{provider} {version} is unsupported; this timeline may be incomplete.', { provider: run.provider, version: run.providerVersion || t('unknown version') });
      timelineWarning.classList.remove('hidden');
    } else {
      timelineWarning.textContent = '';
      timelineWarning.classList.add('hidden');
    }

    const repository = repositorySummary(data.changes || {}, fieldsMap(data.evidence.repository));
    const warnings = Number(run.warningCount) || 0;
    const metrics = $('metrics');
    metrics.replaceChildren(
      metric('Process outcome', process.value, process.detail, process.tone),
      metric('Verification verdict', verification.value, verification.detail, verification.tone),
      metric('Repository evidence', repository.value, repository.detail),
      metric('Normalized actions', data.actionCount || 0),
      metric('Provider events', data.eventCount || 0),
      metric('Warnings', warnings, '', warnings > 0 ? 'warn' : '')
    );
    renderEvidence();
    renderTimeline();
    renderRunList();
    renderWorkspaceState();
  }

  async function loadPatchPage(path, cursor = 0, rememberCurrent = false, generation = state.loadGeneration, consumeHistory = false) {
    const selected = state.selected;
    if (!selected || selected.kind !== 'change' || selected.value.path !== path || selected.patchLoading && cursor === selected.patchCursor) return;
    selected.patchLoading = true;
    announceInspector(t('Loading patch for {path}', { path }));
    renderInspector();
    try {
      const snapshotID = state.run.snapshotId;
      const page = await getJSON(`/api/snapshots/${encodeURIComponent(snapshotID)}/patch?path=${encodeURIComponent(path)}&cursor=${cursor}`);
      if (generation !== state.loadGeneration || state.selected !== selected || selected.value.path !== page.path) return;
      if (rememberCurrent) selected.patchHistory.push(selected.patchCursor);
      if (consumeHistory) selected.patchHistory.pop();
      selected.patch = page.patch || '';
      selected.patchError = '';
      selected.patchCursor = cursor;
      selected.patchNextCursor = page.nextCursor === undefined ? null : page.nextCursor;
      selected.patchAttribution = page.attribution;
      announceInspector(t('Patch loaded for {path}', { path }));
    } catch (error) {
      if (generation === state.loadGeneration && state.selected === selected) {
        selected.patchError = t('Patch unavailable: {error}', { error: error instanceof Error ? error.message : String(error) });
        announceInspector(selected.patchError);
        showError(error);
      }
    } finally {
      if (generation === state.loadGeneration && state.selected === selected) {
        selected.patchLoading = false;
        renderInspector();
      }
    }
  }

  async function loadStreamPage(streamName, cursor = 0, rememberCurrent = false, generation = state.loadGeneration, consumeHistory = false, signal) {
    if (generation !== state.loadGeneration) return;
    const stream = state.streams && state.streams[streamName];
    if (!stream || stream.loading || cursor === null) return;
    stream.loading = true;
    renderTimeline();
    try {
      const page = await getJSON(`/api/snapshots/${encodeURIComponent(state.run.snapshotId)}/${streamName}?cursor=${cursor}`, signal);
      if (generation !== state.loadGeneration) return;
      if (cursor !== stream.currentCursor) state.activeTypes.clear();
      if (rememberCurrent) stream.history.push(stream.currentCursor);
      if (consumeHistory) stream.history.pop();
      stream.items = page.items || [];
      stream.error = '';
      stream.currentCursor = cursor;
      stream.nextCursor = page.nextCursor === undefined ? null : page.nextCursor;
      stream.loaded = true;
      if (streamName === 'changes') {
        stream.total = page.total || 0;
        stream.status = page.status || '';
        stream.reason = page.reason || '';
        stream.attribution = page.attribution || '';
        stream.baseline = page.baseline || '';
        $('change-count').textContent = stream.status === 'unavailable' ? (stream.total === 0 ? '?' : `${stream.total}+?`) : String(stream.total);
      }
    } catch (error) {
      if (generation === state.loadGeneration) {
        stream.error = error instanceof Error ? error.message : String(error);
        showError(error);
      }
    } finally {
      stream.loading = false;
      if (generation === state.loadGeneration) renderTimeline();
    }
  }

  // quiet loads (auto-selection) report failure in the empty state rather than a toast, so the poll can retry without nagging.
  async function loadRun(id, quiet = false) {
    if (state.runAbortController) state.runAbortController.abort();
    const controller = new AbortController();
    state.runAbortController = controller;
    const generation = ++state.loadGeneration;
    try {
      const run = await getJSON(`/api/runs/${encodeURIComponent(id)}`, controller.signal);
      if (generation !== state.loadGeneration) return;
      state.run = run;
      state.streams = {
        actions: { items: [], currentCursor: 0, nextCursor: state.run.actionCount === 0 ? null : 0, history: [], loading: false, loaded: state.run.actionCount === 0, error: '' },
        changes: { items: [], currentCursor: 0, nextCursor: 0, history: [], loading: false, loaded: false, error: '', total: 0, attribution: '', baseline: '', status: '', reason: '' },
        events: { items: [], currentCursor: 0, nextCursor: state.run.eventCount === 0 ? null : 0, history: [], loading: false, loaded: state.run.eventCount === 0, error: '' }
      };
      state.activeTypes.clear();
      state.selected = null;
      renderRun();
      await loadStreamPage(state.mode, 0, false, generation, false, controller.signal);
      if (state.mode !== 'actions') await loadStreamPage('actions', 0, false, generation, false, controller.signal);
      if (state.mode !== 'changes') await loadStreamPage('changes', 0, false, generation, false, controller.signal);
    } catch (error) {
      if (error.name === 'AbortError' || generation !== state.loadGeneration) return;
      if (quiet && !state.run) {
        $('workspace-empty-title').textContent = t('Could not load the latest run');
        $('workspace-empty-body').textContent = t('{error} — retrying; pick a run from the list to try another.', { error: error instanceof Error ? error.message : String(error) });
      } else {
        showError(error);
      }
    } finally {
      if (state.runAbortController === controller) state.runAbortController = null;
    }
  }

  function applyRunList(list) {
    const runs = list.runs || [];
    // ponytail: rebuild the list only when its content changed; a rebuild mid-click would swallow the click.
    const signature = JSON.stringify(runs.map((run) => [run.id, run.provider, run.project, run.statusClass, run.statusLabel, run.startedAt]));
    const changed = signature !== state.runsSignature;
    state.runsSignature = signature;
    state.runs = runs;
    const warning = $('unreadable-warning');
    if (list.unreadable) {
      warning.textContent = t('{n} unreadable run(s) were excluded.', { n: list.unreadable });
      warning.classList.remove('hidden');
    } else {
      warning.classList.add('hidden');
    }
    if (changed) {
      renderRunList();
    } else {
      const byID = new Map(runs.map((run) => [run.id, run]));
      document.querySelectorAll('.run-item').forEach((button) => {
        const run = byID.get(button.dataset.runId);
        if (run) button.querySelector('.run-time').textContent = relativeTime(run.startedAt);
      });
    }
    renderWorkspaceState();
  }

  // Selects the initial or newest run when nothing is shown; a failed attempt is retried quietly on the next poll.
  function autoSelect(list) {
    if (state.run || state.runAbortController || state.runs.length === 0) return;
    return loadRun(list.initialRunId || state.runs[0].id, true);
  }

  // Live refresh: re-fetch /api/runs and redraw the list in place. Selection, focus and the loaded run are untouched.
  async function refreshRuns() {
    if (state.pollController || document.visibilityState === 'hidden') return;
    state.pollController = new AbortController();
    try {
      const list = await getJSON('/api/runs', state.pollController.signal);
      applyRunList(list);
      await autoSelect(list);
    } catch (error) {
      // ponytail: poll failures stay quiet; the next tick retries and user-initiated loads still surface errors.
    } finally {
      state.pollController = null;
    }
  }

  function startPolling() {
    stopPolling();
    state.pollTimer = window.setInterval(refreshRuns, POLL_MS);
  }

  function stopPolling() {
    window.clearInterval(state.pollTimer);
    state.pollTimer = null;
  }

  async function init() {
    setLang(detectLang());
    $('top-meta').textContent = t('Loading recorded evidence…');
    $('workspace-empty-title').textContent = t('Loading recorded evidence…');
    try {
      const list = await getJSON('/api/runs');
      applyRunList(list);
      await autoSelect(list);
    } catch (error) {
      $('workspace-empty-title').textContent = t('Could not load recorded runs');
      $('workspace-empty-body').textContent = error instanceof Error ? error.message : String(error);
      showError(error);
    }
    startPolling();
  }

  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'hidden') {
      stopPolling();
    } else {
      refreshRuns();
      startPolling();
    }
  });

  // Switching language re-renders from state; the timeline selection resets, everything else stays.
  $('lang').addEventListener('change', (event) => {
    setLang(event.target.value);
    if (state.run) renderRun(); else { renderRunList(); renderWorkspaceState(); }
  });
  $('run-search').addEventListener('input', renderRunList);
  $('timeline-search').addEventListener('input', (event) => {
    window.clearTimeout(state.searchTimer);
    state.searchTimer = window.setTimeout(() => {
      state.query = event.target.value.trim().toLowerCase();
      renderTimeline();
    }, 180);
  });
  const tabs = Array.from(document.querySelectorAll('.tab'));
  tabs.forEach((tab, index) => {
    tab.addEventListener('click', async () => {
      tabs.forEach((item) => {
        item.classList.toggle('active', item === tab);
        item.setAttribute('aria-selected', item === tab ? 'true' : 'false');
        item.tabIndex = item === tab ? 0 : -1;
      });
      $('timeline').setAttribute('aria-labelledby', tab.id);
      state.mode = tab.dataset.mode;
      state.activeTypes.clear();
      renderTimeline();
      if (state.streams && !state.streams[state.mode].loaded) await loadStreamPage(state.mode);
    });
    tab.addEventListener('keydown', (event) => {
      let next = index;
      if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
      else if (event.key === 'ArrowLeft') next = (index - 1 + tabs.length) % tabs.length;
      else if (event.key === 'Home') next = 0;
      else if (event.key === 'End') next = tabs.length - 1;
      else return;
      event.preventDefault();
      tabs[next].focus();
      tabs[next].click();
    });
  });

  init();
})();
