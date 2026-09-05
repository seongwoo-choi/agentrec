(() => {
  'use strict';

  const POLL_MS = 5000;
  const LIVE_MS = 3000;
  const SEARCH_MS = 400;
  const state = { lang: 'en', runs: [], runTotal: 0, runNextCursor: '', runGeneration: '', run: null, mode: 'actions', query: '', activeTypes: new Set(), selected: null, streams: null, searchTimer: null, loadGeneration: 0, runAbortController: null, pollTimer: null, pollController: null, runsSignature: '', toastTimer: null, confirmDelete: false, token: '', allowRun: false, storeBytes: 0, trashBytes: 0 };
  const $ = (id) => document.getElementById(id);
  const node = (tag, className, text) => {
    const el = document.createElement(tag);
    if (className) el.className = className;
    if (text !== undefined) el.textContent = text;
    return el;
  };

  // ── Localization ──────────────────────────────────────────────────────────
  // English strings are the keys. Only page-authored copy and the server sentences special-cased below are translated;
  // provider content (commands, paths, prompts, ids) never is. The documented status tokens (NOT RUN, PASS, …) are shown
  // as words through STATUS_WORDS, with the English token kept in the element's title.
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
      'No loaded runs match these filters.': '로드된 실행 중 필터에 맞는 항목이 없습니다.',
      'No loaded runs match this search and these filters.': '로드된 실행 중 검색어와 필터에 모두 맞는 항목이 없습니다.',
      'All exits': '모든 종료 상태',
      'All verification': '모든 검증 상태',
      'Filter by exit reason': '종료 상태로 필터링',
      'Filter by verification': '검증 상태로 필터링',
      'No runs recorded yet': '기록된 실행이 없습니다',
      'No run selected': '선택된 실행이 없습니다',
      'Start a Claude Code or Codex session; it appears here when it ends.': 'Claude Code 또는 Codex 세션을 시작하면 종료 시 여기에 표시됩니다.',
      'Pick a run from the list to inspect its recorded evidence.': '목록에서 실행을 선택하면 기록된 증거를 확인할 수 있습니다.',
      'No recorded runs': '기록된 실행 없음',
      '{n} recorded run(s)': '기록된 실행 {n}개',
      '{size} on disk': '디스크 사용량 {size}',
      '{size} in the trash': '휴지통 {size}',
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
      'Loaded {loaded} of {total}': '{total}개 중 {loaded}개 불러옴',
      'Load more': '더 불러오기',
      'Loading…': '불러오는 중…',
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
      Source: '자료 출처',
      completed: '완료',
      failed: '실패',
      in_progress: '진행 중',
      Model: '모델',
      'Input Tokens': '입력 토큰',
      'Cached Input Tokens': '캐시 읽기 입력 토큰',
      'Cache Creation Input Tokens': '캐시 생성 입력 토큰',
      'Output Tokens': '출력 토큰',
      'Cost USD': '비용(USD)',
      "the provider's transcript, read at session end (the provider's own format, undocumented)": 'provider의 transcript를 세션 종료 시 읽음 (provider 고유 형식, 비문서화)',
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
      'unknown version': '알 수 없는 버전',
      'Delete run': '실행 삭제',
      'Delete this run?': '이 실행을 삭제하시겠습니까?',
      Delete: '삭제',
      Cancel: '취소',
      'Run deleted': '실행을 삭제했습니다',
      Undo: '실행 취소',
      'Cannot delete: {error}': '삭제할 수 없습니다: {error}',
      'Cannot restore: {error}': '복원할 수 없습니다: {error}',
      'This run is still open; it can be deleted after the session ends.': '이 실행은 아직 열려 있습니다. 세션이 끝난 뒤 삭제할 수 있습니다.',
      'Move this run to the trash': '이 실행을 휴지통으로 이동',
      'Compare runners': '러너 비교',
      Close: '닫기',
      'Records one task once per runner, each in a private checkout of the same committed baseline.': '하나의 작업을 러너별로 한 번씩, 같은 커밋 기준점의 독립된 체크아웃에서 기록합니다.',
      'Repository path': '저장소 경로',
      Task: '작업',
      'Describe the task for the runners': '러너에게 줄 작업 설명',
      Runners: '러너',
      unavailable: '사용 불가',
      'Equivalent command': '동일한 명령',
      'Run it inside the repository with the task saved as task.md.': '작업을 task.md로 저장한 뒤 저장소 안에서 실행합니다.',
      Copy: '복사',
      Copied: '복사됨',
      Run: '실행',
      'Start the viewer with `agentrec start --allow-run` to run comparisons from here': '여기에서 비교를 실행하려면 뷰어를 `agentrec start --allow-run`으로 시작합니다',
      'Cannot run comparison: {error}': '비교를 실행할 수 없습니다: {error}',
      'Cannot cancel: {error}': '취소할 수 없습니다: {error}',
      'Could not load comparison status: {error}': '비교 상태를 불러오지 못했습니다: {error}',
      running: '실행 중',
      cancelled: '취소됨',
      'Elapsed {time}': '경과 {time}',
      'Open run {id}': '실행 {id} 열기',
      'Comparison log': '비교 로그',
      'Older output was dropped to keep the log under 1 MiB.': '로그를 1 MiB 이하로 유지하기 위해 오래된 출력을 버렸습니다.',
      'The server truncated the output.': '서버가 출력을 잘라냈습니다.',
      'A comparison needs both runners': '비교에는 두 러너가 모두 필요합니다',
      'Search all runs': '모든 실행 검색',
      'Search results': '검색 결과',
      'Searching…': '검색 중…',
      'Search failed: {error}': '검색에 실패했습니다: {error}',
      'No matches for this search.': '검색 결과가 없습니다.',
      '{n} hit(s) in {m} run(s)': '실행 {m}개에서 {n}건 일치',
      'Results truncated': '결과가 잘렸습니다',
      action: '액션',
      run: '실행',
      'Live · updated {time}': '실시간 · {time} 갱신',
      'Working tree now — measured at {time}, observed during the run, not proof the agent caused it': '현재 작업 트리 — {time} 측정, 실행 중에 관측된 것으로 에이전트가 원인이라는 증명은 아닙니다',
      'Working tree': '작업 트리',
      'WORKING TREE STATUS': '작업 트리 상태',
      'Compare with…': '다른 실행과 비교…',
      'Compare runs': '실행 비교',
      'Pick another recorded run to compare with {id}.': '{id}과(와) 비교할 다른 실행을 선택합니다.',
      'Find a run to compare': '비교할 실행 검색',
      'No other runs to compare.': '비교할 다른 실행이 없습니다.',
      'Loading comparison…': '비교 불러오는 중…',
      'Could not load comparison: {error}': '비교를 불러오지 못했습니다: {error}',
      'Pick another run': '다른 실행 선택',
      'This run': '이 실행',
      'Provider version': '프로바이더 버전',
      Started: '시작',
      'Actions by type': '유형별 액션',
      'Files changed': '변경된 파일',
      'Repository status': '저장소 상태',
      'Only in {id}': '{id}에만 있음',
      'Some changed files were not read; this split is incomplete.': '변경된 파일 일부를 읽지 않았습니다. 이 구분은 완전하지 않습니다.',
      'In both': '양쪽 모두',
      'No files': '파일 없음',
      '+{n} more': '+{n}개 더',
      'counted from the first {n} actions': '처음 {n}개 액션 기준',
      'Verify now': '지금 검증',
      'Verifying…': '검증 중…',
      'Verified later': '사후 검증',
      'This run was not verified when it ended.': '이 run은 끝났을 때 검증되지 않았습니다.',
      'Measured at {time}': '{time} 측정',
      'Runs the checks committed in the repository now, in the run\'s repository.': '실행의 저장소에서, 커밋된 검증 체크를 지금 실행합니다.',
      'Run later, against the repository as it is now — not the state the run left behind.': '나중에 현재 상태의 저장소를 대상으로 실행한 결과입니다. 실행이 남긴 상태가 아닙니다.',
      'Run later, against the repository as it is now — not the state the run left behind; the repository HEAD has moved since.': '나중에 현재 상태의 저장소를 대상으로 실행한 결과입니다. 실행이 남긴 상태가 아니며, 그 사이 저장소 HEAD가 바뀌었습니다.',
      'Run later, against the repository as it is now — not the state the run left behind; the repository HEAD has not moved since.': '나중에 현재 상태의 저장소를 대상으로 실행한 결과입니다. 실행이 남긴 상태가 아니며, 그 사이 저장소 HEAD는 바뀌지 않았습니다.',
      'Run later, against the repository as it is now — not the state the run left behind; whether the repository HEAD moved since is not known.': '나중에 현재 상태의 저장소를 대상으로 실행한 결과입니다. 실행이 남긴 상태가 아니며, 그 사이 저장소 HEAD가 바뀌었는지는 알 수 없습니다.',
      'Cannot verify: {error}': '검증할 수 없습니다: {error}',
      'Observed by verification checks, run later': '검증 체크가 관측 (사후 실행)'
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
      'No loaded runs match these filters.': '読み込み済みの実行にフィルターと一致する項目がありません。',
      'No loaded runs match this search and these filters.': '読み込み済みの実行に検索語とフィルターの両方に一致する項目がありません。',
      'All exits': 'すべての終了状態',
      'All verification': 'すべての検証状態',
      'Filter by exit reason': '終了状態で絞り込む',
      'Filter by verification': '検証状態で絞り込む',
      'No runs recorded yet': '記録された実行はありません',
      'No run selected': '実行が選択されていません',
      'Start a Claude Code or Codex session; it appears here when it ends.': 'Claude Code または Codex のセッションを開始すると、終了時にここに表示されます。',
      'Pick a run from the list to inspect its recorded evidence.': '一覧から実行を選ぶと、記録された証跡を確認できます。',
      'No recorded runs': '記録された実行なし',
      '{n} recorded run(s)': '記録された実行 {n} 件',
      '{size} on disk': 'ディスク使用量 {size}',
      '{size} in the trash': 'ゴミ箱 {size}',
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
      'Loaded {loaded} of {total}': '{total} 件中 {loaded} 件を読み込み済み',
      'Load more': 'さらに読み込む',
      'Loading…': '読み込んでいます…',
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
      Source: '取得元',
      completed: '完了',
      failed: '失敗',
      in_progress: '進行中',
      Model: 'モデル',
      'Input Tokens': '入力トークン',
      'Cached Input Tokens': 'キャッシュ読み取り入力トークン',
      'Cache Creation Input Tokens': 'キャッシュ作成入力トークン',
      'Output Tokens': '出力トークン',
      'Cost USD': 'コスト (USD)',
      "the provider's transcript, read at session end (the provider's own format, undocumented)": 'プロバイダーのトランスクリプトをセッション終了時に読み取り（プロバイダー独自の形式、非公開仕様）',
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
      'unknown version': '不明なバージョン',
      'Delete run': '実行を削除',
      'Delete this run?': 'この実行を削除しますか?',
      Delete: '削除',
      Cancel: 'キャンセル',
      'Run deleted': '実行を削除しました',
      Undo: '元に戻す',
      'Cannot delete: {error}': '削除できません: {error}',
      'Cannot restore: {error}': '復元できません: {error}',
      'This run is still open; it can be deleted after the session ends.': 'この実行はまだ開いています。セッション終了後に削除できます。',
      'Move this run to the trash': 'この実行をごみ箱へ移動',
      'Compare runners': 'ランナー比較',
      Close: '閉じる',
      'Records one task once per runner, each in a private checkout of the same committed baseline.': '1つのタスクをランナーごとに1回ずつ、同じコミット済みベースラインの専用チェックアウトで記録します。',
      'Repository path': 'リポジトリのパス',
      Task: 'タスク',
      'Describe the task for the runners': 'ランナーに与えるタスクの説明',
      Runners: 'ランナー',
      unavailable: '利用不可',
      'Equivalent command': '同等のコマンド',
      'Run it inside the repository with the task saved as task.md.': 'タスクを task.md として保存し、リポジトリ内で実行します。',
      Copy: 'コピー',
      Copied: 'コピーしました',
      Run: '実行',
      'Start the viewer with `agentrec start --allow-run` to run comparisons from here': 'ここから比較を実行するには、ビューアを `agentrec start --allow-run` で起動します',
      'Cannot run comparison: {error}': '比較を実行できません: {error}',
      'Cannot cancel: {error}': 'キャンセルできません: {error}',
      'Could not load comparison status: {error}': '比較の状態を読み込めませんでした: {error}',
      running: '実行中',
      cancelled: 'キャンセル済み',
      'Elapsed {time}': '経過 {time}',
      'Open run {id}': '実行 {id} を開く',
      'Comparison log': '比較ログ',
      'Older output was dropped to keep the log under 1 MiB.': 'ログを 1 MiB 以下に保つため、古い出力を破棄しました。',
      'The server truncated the output.': 'サーバーが出力を切り詰めました。',
      'A comparison needs both runners': '比較には両方のランナーが必要です',
      'Search all runs': 'すべての実行を検索',
      'Search results': '検索結果',
      'Searching…': '検索しています…',
      'Search failed: {error}': '検索に失敗しました: {error}',
      'No matches for this search.': '検索に一致するものはありません。',
      '{n} hit(s) in {m} run(s)': '{m} 件の実行で {n} 件が一致',
      'Results truncated': '結果は切り詰められています',
      action: 'アクション',
      run: '実行',
      'Live · updated {time}': 'ライブ · {time} 更新',
      'Working tree now — measured at {time}, observed during the run, not proof the agent caused it': '現在の作業ツリー — {time} に計測。実行中に観測されたもので、エージェントが原因であることの証明ではありません',
      'Working tree': '作業ツリー',
      'WORKING TREE STATUS': '作業ツリーの状態',
      'Compare with…': '他の実行と比較…',
      'Compare runs': '実行の比較',
      'Pick another recorded run to compare with {id}.': '{id} と比較する別の実行を選びます。',
      'Find a run to compare': '比較する実行を検索',
      'No other runs to compare.': '比較できる他の実行はありません。',
      'Loading comparison…': '比較を読み込んでいます…',
      'Could not load comparison: {error}': '比較を読み込めませんでした: {error}',
      'Pick another run': '別の実行を選ぶ',
      'This run': 'この実行',
      'Provider version': 'プロバイダーのバージョン',
      Started: '開始',
      'Actions by type': '種類別アクション',
      'Files changed': '変更されたファイル',
      'Repository status': 'リポジトリの状態',
      'Only in {id}': '{id} のみ',
      'Some changed files were not read; this split is incomplete.': '変更されたファイルの一部は読み取っていません。この区分は完全ではありません。',
      'In both': '両方にあり',
      'No files': 'ファイルなし',
      '+{n} more': '他 {n} 件',
      'counted from the first {n} actions': '最初の {n} 件のアクションで集計',
      'Verify now': '今すぐ検証',
      'Verifying…': '検証しています…',
      'Verified later': '事後検証',
      'This run was not verified when it ended.': 'この run は終了時に検証されていません。',
      'Measured at {time}': '{time} に計測',
      'Runs the checks committed in the repository now, in the run\'s repository.': '実行のリポジトリで、コミット済みの検証チェックを今すぐ実行します。',
      'Run later, against the repository as it is now — not the state the run left behind.': '後から、現在の状態のリポジトリに対して実行した結果です。実行が残した状態ではありません。',
      'Run later, against the repository as it is now — not the state the run left behind; the repository HEAD has moved since.': '後から、現在の状態のリポジトリに対して実行した結果です。実行が残した状態ではなく、その後リポジトリの HEAD は移動しています。',
      'Run later, against the repository as it is now — not the state the run left behind; the repository HEAD has not moved since.': '後から、現在の状態のリポジトリに対して実行した結果です。実行が残した状態ではなく、その後リポジトリの HEAD は移動していません。',
      'Run later, against the repository as it is now — not the state the run left behind; whether the repository HEAD moved since is not known.': '後から、現在の状態のリポジトリに対して実行した結果です。実行が残した状態ではなく、その後リポジトリの HEAD が移動したかどうかは分かりません。',
      'Cannot verify: {error}': '検証できません: {error}',
      'Observed by verification checks, run later': '検証チェックが観測（事後実行）'
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
      'No loaded runs match these filters.': '已加载的运行中没有符合筛选条件的项目。',
      'No loaded runs match this search and these filters.': '已加载的运行中没有同时符合搜索词和筛选条件的项目。',
      'All exits': '所有退出状态',
      'All verification': '所有验证状态',
      'Filter by exit reason': '按退出状态筛选',
      'Filter by verification': '按验证状态筛选',
      'No runs recorded yet': '尚未记录任何运行',
      'No run selected': '未选择运行',
      'Start a Claude Code or Codex session; it appears here when it ends.': '启动 Claude Code 或 Codex 会话，结束后会显示在这里。',
      'Pick a run from the list to inspect its recorded evidence.': '从列表中选择一个运行以查看其记录的证据。',
      'No recorded runs': '没有记录的运行',
      '{n} recorded run(s)': '已记录 {n} 个运行',
      '{size} on disk': '占用磁盘 {size}',
      '{size} in the trash': '回收站 {size}',
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
      'Loaded {loaded} of {total}': '已加载 {loaded} / {total}',
      'Load more': '加载更多',
      'Loading…': '正在加载…',
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
      Source: '来源',
      completed: '已完成',
      failed: '失败',
      in_progress: '进行中',
      Model: '模型',
      'Input Tokens': '输入 token',
      'Cached Input Tokens': '缓存读取输入 token',
      'Cache Creation Input Tokens': '缓存创建输入 token',
      'Output Tokens': '输出 token',
      'Cost USD': '费用（USD）',
      "the provider's transcript, read at session end (the provider's own format, undocumented)": '提供方的会话记录，在会话结束时读取（提供方自有格式，未文档化）',
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
      'unknown version': '未知版本',
      'Delete run': '删除运行',
      'Delete this run?': '删除此运行？',
      Delete: '删除',
      Cancel: '取消',
      'Run deleted': '运行已删除',
      Undo: '撤销',
      'Cannot delete: {error}': '无法删除：{error}',
      'Cannot restore: {error}': '无法恢复：{error}',
      'This run is still open; it can be deleted after the session ends.': '此运行仍在进行中；会话结束后才能删除。',
      'Move this run to the trash': '将此运行移至回收站',
      'Compare runners': '比较运行器',
      Close: '关闭',
      'Records one task once per runner, each in a private checkout of the same committed baseline.': '对同一任务按运行器各记录一次，每次都在同一已提交基线的独立检出中进行。',
      'Repository path': '仓库路径',
      Task: '任务',
      'Describe the task for the runners': '给运行器的任务描述',
      Runners: '运行器',
      unavailable: '不可用',
      'Equivalent command': '等效命令',
      'Run it inside the repository with the task saved as task.md.': '将任务保存为 task.md 后在仓库内运行。',
      Copy: '复制',
      Copied: '已复制',
      Run: '运行',
      'Start the viewer with `agentrec start --allow-run` to run comparisons from here': '要从这里运行比较，请使用 `agentrec start --allow-run` 启动查看器',
      'Cannot run comparison: {error}': '无法运行比较：{error}',
      'Cannot cancel: {error}': '无法取消：{error}',
      'Could not load comparison status: {error}': '无法加载比较状态：{error}',
      running: '运行中',
      cancelled: '已取消',
      'Elapsed {time}': '已用时 {time}',
      'Open run {id}': '打开运行 {id}',
      'Comparison log': '比较日志',
      'Older output was dropped to keep the log under 1 MiB.': '为使日志保持在 1 MiB 以下，已丢弃较早的输出。',
      'The server truncated the output.': '服务器截断了输出。',
      'A comparison needs both runners': '比较需要两个运行器',
      'Search all runs': '搜索所有运行',
      'Search results': '搜索结果',
      'Searching…': '正在搜索…',
      'Search failed: {error}': '搜索失败：{error}',
      'No matches for this search.': '没有匹配的结果。',
      '{n} hit(s) in {m} run(s)': '在 {m} 个运行中找到 {n} 条匹配',
      'Results truncated': '结果已截断',
      action: '操作',
      run: '运行',
      'Live · updated {time}': '实时 · {time} 更新',
      'Working tree now — measured at {time}, observed during the run, not proof the agent caused it': '当前工作树 — 测量于 {time}，在运行期间观测到，并非代理造成的证明',
      'Working tree': '工作树',
      'WORKING TREE STATUS': '工作树状态',
      'Compare with…': '与其他运行比较…',
      'Compare runs': '比较运行',
      'Pick another recorded run to compare with {id}.': '选择另一个运行与 {id} 进行比较。',
      'Find a run to compare': '搜索要比较的运行',
      'No other runs to compare.': '没有其他可比较的运行。',
      'Loading comparison…': '正在加载比较…',
      'Could not load comparison: {error}': '无法加载比较：{error}',
      'Pick another run': '选择其他运行',
      'This run': '当前运行',
      'Provider version': '提供方版本',
      Started: '开始时间',
      'Actions by type': '按类型统计的操作',
      'Files changed': '变更的文件',
      'Repository status': '仓库状态',
      'Only in {id}': '仅在 {id} 中',
      'Some changed files were not read; this split is incomplete.': '部分改动文件未被读取，此划分并不完整。',
      'In both': '两者都有',
      'No files': '没有文件',
      '+{n} more': '还有 {n} 个',
      'counted from the first {n} actions': '按前 {n} 个操作统计',
      'Verify now': '立即验证',
      'Verifying…': '正在验证…',
      'Verified later': '事后验证',
      'This run was not verified when it ended.': '这条运行记录在结束时没有经过验证。',
      'Measured at {time}': '测量于 {time}',
      'Runs the checks committed in the repository now, in the run\'s repository.': '在该运行的仓库中，立即运行已提交的验证检查。',
      'Run later, against the repository as it is now — not the state the run left behind.': '事后针对仓库当前状态运行的结果，并非该运行留下的状态。',
      'Run later, against the repository as it is now — not the state the run left behind; the repository HEAD has moved since.': '事后针对仓库当前状态运行的结果，并非该运行留下的状态；此后仓库 HEAD 已发生变化。',
      'Run later, against the repository as it is now — not the state the run left behind; the repository HEAD has not moved since.': '事后针对仓库当前状态运行的结果，并非该运行留下的状态；此后仓库 HEAD 没有变化。',
      'Run later, against the repository as it is now — not the state the run left behind; whether the repository HEAD moved since is not known.': '事后针对仓库当前状态运行的结果，并非该运行留下的状态；此后仓库 HEAD 是否变化未知。',
      'Cannot verify: {error}': '无法验证：{error}',
      'Observed by verification checks, run later': '由验证检查观测（事后运行）'
    }
  };

  // The documented status tokens, shown as words in the other UI languages. statusNode keeps the English token in the title.
  const STATUS_WORDS = {
    ko: { PASS: '통과', FAIL: '실패', RUNNING: '실행 중', ENDED: '종료', LOST: '유실', UNKNOWN: '알 수 없음', 'NOT RUN': '미실행', 'NOT OBSERVED': '미관측', 'NOT RECORDED': '미기록', PENDING: '측정 대기', AVAILABLE: '측정됨', TIMEOUT: '시간 초과', ERROR: '오류', TAINTED: '오염됨' },
    ja: { PASS: '合格', FAIL: '不合格', RUNNING: '実行中', ENDED: '終了', LOST: '消失', UNKNOWN: '不明', 'NOT RUN': '未検証', 'NOT OBSERVED': '未観測', 'NOT RECORDED': '未記録', PENDING: '計測待ち', AVAILABLE: '計測済み', TIMEOUT: 'タイムアウト', ERROR: 'エラー', TAINTED: '汚染あり' },
    'zh-CN': { PASS: '通过', FAIL: '失败', RUNNING: '运行中', ENDED: '已结束', LOST: '已丢失', UNKNOWN: '未知', 'NOT RUN': '未运行', 'NOT OBSERVED': '未观测', 'NOT RECORDED': '未记录', PENDING: '待测量', AVAILABLE: '已测量', TIMEOUT: '超时', ERROR: '错误', TAINTED: '已污染' }
  };
  const STATUS_TOKENS = new Set(Object.keys(STATUS_WORDS.ko));

  function statusWord(token) {
    const table = STATUS_WORDS[state.lang];
    return (table && table[token]) || token;
  }

  // statusToken maps a server label to its documented token: session outcomes and the legacy UNAVAILABLE (now NOT RUN) included.
  function statusToken(label) {
    const raw = String(label || '');
    const known = OUTCOMES[raw.toLowerCase()];
    if (known && known.value) return known.value;
    const upper = raw.toUpperCase();
    if (upper === 'UNAVAILABLE') return 'NOT RUN';
    return STATUS_TOKENS.has(upper) ? upper : raw;
  }

  function statusNode(tag, className, token, explanation) {
    const word = statusWord(token);
    const el = node(tag, className, word);
    const title = [word === token ? '' : token, explanation || ''].filter(Boolean).join(': ');
    if (title) el.title = title;
    return el;
  }

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
    document.querySelectorAll('[data-i18n-label]').forEach((el) => { el.setAttribute('aria-label', t(el.dataset.i18nLabel)); });
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

  // getJSONRetrying re-asks a few times when the server says its snapshot lost a race with the recorder ("…; retry"):
  // a run that is still being written changes under the capture, and the next attempt usually lands between writes.
  async function getJSONRetrying(path, signal, attempts = 3) {
    for (let attempt = 1; ; attempt += 1) {
      try {
        return await getJSON(path, signal);
      } catch (error) {
        if (attempt >= attempts || error.name === 'AbortError' || !/retry/.test(error.message)) throw error;
        await new Promise((resolve) => window.setTimeout(resolve, 300 * attempt));
      }
    }
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

  function humanBytes(n) {
  if (n < 1024) return `${n} B`;
  let v = n;
  for (const unit of ['KB', 'MB', 'GB', 'TB']) {
    v /= 1024;
    if (v < 1024 || unit === 'TB') return `${v < 10 ? v.toFixed(1) : Math.round(v)} ${unit}`;
  }
  return '';
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
    if (v === 'tainted') return 'warn';
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
    'verification_observed (post-hoc)': () => t('Observed by verification checks, run later'),
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
    session_lost: { value: 'LOST', tone: 'fail', detail: 'No hook arrived for the idle timeout, or the recorder was signalled; the session\'s own end was not seen.' },
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
    'NOT RUN': NO_VERIFICATION
  };
  // Old bundles and servers still say UNAVAILABLE where the current words are NOT RUN (verification) and NOT RECORDED (repository).
  const verdictWord = (value) => (value === 'UNAVAILABLE' ? 'NOT RUN' : value);
  const repositoryWord = (value) => (value === 'UNAVAILABLE' ? 'NOT RECORDED' : value);
  const EMPTY_WORD = { Verification: 'NOT RUN', 'Repository delta': 'NOT RECORDED' };
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
    if (!fields || fields.length === 0) return { value: 'NOT RUN', tone: '', detail: t(NO_VERIFICATION) };
    const map = fieldsMap(fields);
    const status = verdictWord(String(map.get('Status') || 'NOT RUN').toUpperCase());
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
    return { value: 'NOT RECORDED', detail: sentence(changes.reason) || t(REPOSITORY_NONE) };
  }

  // explainStatus is the hover text for the server-classified run-list chip.
  function explainStatus(label) {
    const v = verdictWord(String(label || '').toUpperCase());
    if (v === 'NOT RUN') return t('No checks were run for this session.');
    if (v === 'PASS') return t('Verification checks passed.');
    if (v === 'FAIL') return t('Verification checks failed.');
    const known = OUTCOMES[String(label || '').toLowerCase()];
    return known && known.value ? t(known.detail) : t('The run ended with {label}.', { label });
  }

  // runItem is one card of a run list; the sidebar and the compare-runs picker draw the same card.
  function runItem(run, active) {
    const button = node('button', `run-item${active ? ' active' : ''}`);
    button.type = 'button';
    button.dataset.runId = run.id;
    if (active) button.setAttribute('aria-current', 'true');
    const head = node('div', 'run-item-head');
    head.append(node('span', 'run-project', run.project || t('unknown project')), node('span', 'run-time', relativeTime(run.startedAt)));
    const foot = node('div', 'run-item-foot');
    const status = statusNode('span', `mini-status ${run.statusClass}`, statusToken(run.statusLabel), explainStatus(run.statusLabel));
    foot.append(node('span', 'run-provider', run.provider || t('unknown')), status);
    button.append(head, node('div', 'run-id', shortID(run.id)), foot);
    return button;
  }

  function syncRunFilter(id, values, allLabel) {
    const select = $(id);
    const selected = select.value;
    const all = node('option', '', t(allLabel));
    all.value = '';
    select.replaceChildren(all);
    for (const value of [...new Set(values.filter((value) => typeof value === 'string' && value))].sort()) {
      const option = node('option', '', value);
      option.value = value;
      select.append(option);
    }
    if (Array.from(select.options).some((option) => option.value === selected)) select.value = selected;
  }

  const runMatches = (run, query, exit, verification) =>
    (!query || `${run.id} ${run.provider} ${run.project} ${run.exit} ${run.verification}`.toLowerCase().includes(query))
    && (!exit || run.exit === exit)
    && (!verification || run.verification === verification);

  function renderRunList() {
    const query = $('run-search').value.trim().toLowerCase();
    syncRunFilter('run-exit-filter', state.runs.map((run) => run.exit), 'All exits');
    syncRunFilter('run-verification-filter', state.runs.map((run) => run.verification), 'All verification');
    const exit = $('run-exit-filter').value;
    const verification = $('run-verification-filter').value;
    const list = $('run-list');
    const focused = document.activeElement && document.activeElement.dataset ? document.activeElement.dataset.runId : undefined;
    list.replaceChildren();
    let shown = 0;
    for (const run of state.runs) {
      if (!runMatches(run, query, exit, verification)) continue;
      shown += 1;
      const button = runItem(run, Boolean(state.run && state.run.run.id === run.id));
      button.addEventListener('click', () => loadRun(run.id));
      list.append(button);
      if (focused === run.id) button.focus({ preventScroll: true });
    }
    $('run-count').textContent = String(state.runs.length);
    const more = $('run-load-more');
    more.textContent = t('Load more');
    more.classList.toggle('hidden', !state.runNextCursor);
    const size = $('store-size');
    // What the Delete button fills is part of what the store costs, and it is
    // not freed until the trash is emptied: both numbers or neither.
    const parts = [];
    if (state.storeBytes) parts.push(t('{size} on disk', { size: humanBytes(state.storeBytes) }));
    if (state.trashBytes) parts.push(t('{size} in the trash', { size: humanBytes(state.trashBytes) }));
    size.textContent = parts.join(' · ');
    size.classList.toggle('hidden', parts.length === 0);
    const empty = $('run-list-empty');
    const status = $('run-list-status');
    if (shown === 0) {
      let emptyMessage = 'No loaded runs match these filters.';
      if (state.runs.length === 0) emptyMessage = 'No runs recorded yet — start a Claude Code or Codex session; it appears here when it ends.';
      else if (query && (exit || verification)) emptyMessage = 'No loaded runs match this search and these filters.';
      else if (query) emptyMessage = 'No runs match this search.';
      empty.textContent = t(emptyMessage);
      status.textContent = t(emptyMessage);
      empty.classList.remove('hidden');
    } else {
      status.textContent = '';
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
    // Chips are rebuilt whenever a page lands; the focused chip stays focused.
    const focused = document.activeElement && document.activeElement.dataset ? document.activeElement.dataset.type : undefined;
    holder.replaceChildren();
    const counts = new Map();
    for (const item of items) {
      const type = typeOf(item);
      counts.set(type, (counts.get(type) || 0) + 1);
    }
    for (const [type, count] of [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
      const chip = node('button', `filter-chip${state.activeTypes.size === 0 || state.activeTypes.has(type) ? ' active' : ''}`, `${t(TYPE_LABELS[type] || type)} ${count}`);
      chip.type = 'button';
      chip.dataset.type = type;
      chip.setAttribute('aria-pressed', String(state.activeTypes.size === 0 || state.activeTypes.has(type)));
      chip.addEventListener('click', () => {
        if (state.activeTypes.size === 0) for (const key of counts.keys()) state.activeTypes.add(key);
        if (state.activeTypes.has(type)) state.activeTypes.delete(type); else state.activeTypes.add(type);
        if (state.activeTypes.size === counts.size) state.activeTypes.clear();
        renderTimeline();
      });
      holder.append(chip);
      if (focused === type) chip.focus({ preventScroll: true });
    }
  }

  function matches(item, type, text) {
    if (state.activeTypes.size > 0 && !state.activeTypes.has(type)) return false;
    if (!state.query) return true;
    return text.toLowerCase().includes(state.query);
  }

  // Hook-delivered session events carry hook_event_name instead of type; stubs carry agentrec_dropped.
  const eventType = (event) => (typeof event.type === 'string' && event.type) || (typeof event.hook_event_name === 'string' && event.hook_event_name) || '(untyped)';
  const HOOK_DETAIL = { PostToolUse: 'tool_name', PostToolUseFailure: 'tool_name', UserPromptSubmit: 'prompt', Stop: 'last_assistant_message', SessionStart: 'source', SessionEnd: 'reason' };

  function eventDetail(event) {
    const text = (value) => (typeof value === 'string' ? value.replace(/\s+/g, ' ').trim().slice(0, 180) : '');
    if (text(event.agentrec_dropped)) return [text(event.tool_name), text(event.agentrec_dropped)].filter(Boolean).join(' · ');
    const field = HOOK_DETAIL[event.hook_event_name];
    return field ? text(event[field]) : '';
  }

  // ── Infinite scroll ───────────────────────────────────────────────────────
  // The tail of the timeline holds a sentinel; when it enters the scroll container the next page is appended.
  // ponytail: one observer for the one scroll container; every tail render re-observes a fresh sentinel, which also
  // re-fires when the appended rows still do not fill the container (the filter hid them, or the page was short).
  const observer = typeof IntersectionObserver === 'function' ? new IntersectionObserver((entries) => {
    for (const entry of entries) if (entry.isIntersecting) loadMore(entry.target.dataset.stream);
  }, { root: $('timeline'), rootMargin: '200px 0px' }) : null;

  // loadMore appends the next page; a failed page is retried only from the button, never from the sentinel.
  function loadMore(streamName, manual = false) {
    const stream = state.streams && state.streams[streamName];
    if (!stream || stream.loading || stream.nextCursor === null || (stream.error && !manual)) return;
    loadStreamPage(streamName, stream.nextCursor, true, state.loadGeneration, manual);
  }

  function renderTail(streamName) {
    const timeline = $('timeline');
    const stream = state.streams[streamName];
    if (observer) observer.disconnect();
    timeline.querySelectorAll('.stream-tail').forEach((el) => el.remove());
    if (!stream.loaded && !stream.error) return;
    const tail = node('div', 'stream-tail');
    if (stream.loading) {
      tail.append(node('div', 'timeline-note', t('Loading…')));
    } else if (stream.nextCursor !== null) {
      const sentinel = node('div', 'stream-sentinel');
      sentinel.dataset.stream = streamName;
      const more = node('button', 'load-more', t('Load more'));
      more.type = 'button';
      more.addEventListener('click', () => loadMore(streamName, true));
      // The button is the fallback: no IntersectionObserver, rows that do not overflow the container, or a failed page.
      if (observer && !stream.error && timeline.scrollHeight > timeline.clientHeight) more.classList.add('hidden');
      tail.append(sentinel, more);
    }
    if (stream.items.length) tail.append(node('div', 'pager-label', t('Loaded {loaded} of {total}', { loaded: stream.items.length, total: MODES[streamName].total(stream) })));
    timeline.append(tail);
    const sentinel = tail.querySelector('.stream-sentinel');
    if (observer && sentinel) observer.observe(sentinel);
    if (!observer && !stream.loading && !stream.error && stream.nextCursor !== null && timeline.scrollHeight <= timeline.clientHeight) loadMore(streamName);
  }

  // renderEmpty explains an empty timeline once nothing more can arrive; while pages remain the tail speaks instead.
  function renderEmpty(timeline, streamName) {
    const stream = state.streams[streamName];
    timeline.querySelectorAll('.timeline-empty:not(.stream-error):not(.change-evidence-warning)').forEach((el) => el.remove());
    if (stream.shown > 0 || stream.error) return;
    const message = stream.loaded ? (stream.nextCursor === null ? MODES[streamName].empty(stream) : '') : MODES[streamName].loading;
    if (message) timeline.append(node('div', 'timeline-empty', t(message)));
  }

  // renderRows appends the rows for items[from…] that pass the filter and returns the first one.
  function renderRows(timeline, streamName, from) {
    const mode = MODES[streamName];
    const stream = state.streams[streamName];
    const context = mode.context ? mode.context(stream.items) : null;
    let first = null;
    for (let index = from; index < stream.items.length; index += 1) {
      const row = mode.row(stream.items[index], index, context);
      if (!row) continue;
      stream.shown += 1;
      timeline.append(row);
      first = first || row;
    }
    return first;
  }

  // appendTimeline adds one page's rows below what is shown; selection, focus and scroll position stay.
  function appendTimeline(streamName, from, focusNew) {
    const timeline = $('timeline');
    timeline.querySelectorAll('.stream-tail, .timeline-empty:not(.stream-error):not(.change-evidence-warning)').forEach((el) => el.remove());
    const first = renderRows(timeline, streamName, from);
    renderTypeFilters(state.streams[streamName].items, MODES[streamName].typeOf);
    renderEmpty(timeline, streamName);
    renderTail(streamName);
    if (focusNew) {
      const target = first || timeline.querySelector('.stream-tail .load-more:not(.hidden)');
      if (target) target.focus();
    }
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

  // ── Timeline rows ─────────────────────────────────────────────────────────
  function actionRow(action, index, byID) {
    const type = action.type || 'unknown';
    const speech = conversationText(action);
    const detail = speech === null ? (firstDetail(action.input) || firstDetail(action.result)) : speech.slice(0, 180);
    const searchable = `${type} ${action.provider || ''} ${action.status || ''} ${detail} ${JSON.stringify(action.input || {})}`;
    if (!matches(action, type, searchable)) return null;
    const family = actionFamily(type);
    const row = timelineRow(`action-row${speech === null ? '' : ` conversation-row ${family}`}`, () => selectItem(row, { kind: 'action', value: action }));
    row.style.setProperty('--depth', String(actionDepth(action, byID)));
    row.dataset.index = String(index);
    const time = node('div', 'action-time', clock(action.startedAt));
    if (speech !== null) {
      const body = node('div', 'speech-body');
      body.append(node('div', 'speaker', type === 'user.prompt' ? t('You') : (action.provider || t('provider'))), speechBlock(speech));
      row.append(time, body);
      return row;
    }
    const rail = node('div', 'action-rail');
    rail.append(node('span', `action-dot ${family} ${statusClass(action.status)}`));
    const body = node('div', 'action-body');
    const head = node('div', 'action-head');
    head.append(node('span', 'action-type', type), node('span', 'action-summary', detail || action.id));
    const meta = node('div', 'action-meta');
    meta.append(node('span', 'source-badge', action.provider || t('provider')), node('span', '', t(action.status || 'reported')));
    const elapsed = duration(action);
    if (elapsed) meta.append(node('span', '', elapsed));
    if (action.parentId) meta.append(node('span', '', `↳ ${shortID(action.parentId)}`));
    const observedPaths = action.samePathObserved || [];
    if (observedPaths.length) meta.append(node('span', 'path-correlation', t('same path observed — not causal proof')));
    body.append(head, meta);
    row.append(time, rail, body);
    return row;
  }

  function changeRow(change) {
    const type = changeFamily(change);
    const counts = change.binary ? t('binary') : [change.additions === undefined ? '' : `+${change.additions}`, change.deletions === undefined ? '' : `-${change.deletions}`].filter(Boolean).join(' ');
    if (!matches(change, type, `${type} ${change.path} ${change.kind || ''} ${counts}`)) return null;
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
    return row;
  }

  // liveChangeRow is one working-tree entry of a running run: git's porcelain status code, no counts, no patch.
  function liveChangeRow(file) {
    const status = String(file.status || '').trim() || '?';
    if (!matches(file, status, `${status} ${file.path}`)) return null;
    const untracked = status.startsWith('?');
    const row = timelineRow('action-row change-row', () => selectItem(row, { kind: 'live', value: file }));
    row.dataset.path = file.path;
    const rail = node('div', 'action-rail');
    rail.append(node('span', `action-dot ${untracked ? 'untracked' : 'tracked'}`));
    const body = node('div', 'action-body');
    const head = node('div', 'action-head');
    head.append(node('span', 'action-type', file.path));
    const meta = node('div', 'action-meta');
    meta.append(node('span', '', t(untracked ? 'untracked' : 'tracked')), node('span', 'source-badge', t('Working tree')));
    body.append(head, meta);
    row.append(node('div', `change-marker${untracked ? ' untracked' : ''}`, status), rail, body);
    return row;
  }

  // renderLiveChanges draws the working tree of a running run. A tick redraws it in place: selection, focus and scroll
  // are kept by path, and a selected file that is no longer listed clears the inspector.
  function renderLiveChanges() {
    const timeline = $('timeline');
    const files = live.changes ? live.changes.files || [] : [];
    const selectedPath = state.selected && state.selected.kind === 'live' ? state.selected.value.path : '';
    const focusedPath = document.activeElement && document.activeElement.dataset ? document.activeElement.dataset.path : undefined;
    const scrollTop = timeline.scrollTop;
    timeline.replaceChildren();
    renderTypeFilters(files, (file) => String(file.status || '').trim() || '?');
    if (live.error) timeline.append(node('div', 'timeline-empty stream-error', t('Could not load repository changes: {error}', { error: live.error })));
    if (!live.changes) {
      if (!live.error) timeline.append(node('div', 'timeline-empty', t('Loading repository changes…')));
      return;
    }
    timeline.append(labelled('div', 'timeline-note live-caption', t('Working tree now — measured at {time}, observed during the run, not proof the agent caused it', { time: clock(live.changes.measuredAt) }), live.changes.note));
    let shown = 0;
    let found = false;
    for (const file of files) {
      const row = liveChangeRow(file);
      if (!row) continue;
      shown += 1;
      if (file.path === selectedPath) {
        row.classList.add('selected');
        found = true;
      }
      timeline.append(row);
      if (file.path === focusedPath) row.focus({ preventScroll: true });
    }
    if (selectedPath && !found) {
      state.selected = null;
      renderInspector();
    }
    if (shown === 0) timeline.append(node('div', 'timeline-empty', t(files.length ? 'No loaded changes match this filter.' : 'No repository changes were observed.')));
    timeline.scrollTop = scrollTop;
  }

  function eventRow(event, index) {
    const type = eventType(event);
    const detail = eventDetail(event) || firstDetail(event) || firstDetail(event.message) || event.subtype || event.event || t('event {n}', { n: index + 1 });
    if (!matches(event, type, `${type} ${detail} ${JSON.stringify(event)}`)) return null;
    const row = timelineRow('action-row event-row', () => selectItem(row, { kind: 'event', value: event, index }));
    const time = node('div', 'action-time', clock(event.timestamp || event.created_at || event.createdAt));
    const rail = node('div', 'action-rail');
    rail.append(node('span', `action-dot${event.hook_event_name === 'PostToolUseFailure' ? ' fail' : ''}`));
    const body = node('div', 'action-body');
    const head = node('div', 'action-head');
    head.append(node('span', 'action-type', t(type)), node('span', 'action-summary', detail));
    const meta = node('div', 'action-meta');
    meta.append(node('span', 'source-badge', t('provider event')), node('span', '', `#${index + 1}`));
    body.append(head, meta);
    row.append(time, rail, body);
    return row;
  }

  // MODES is what differs per stream: how a row is typed and built, the stream's size, and the words for an empty timeline.
  const MODES = {
    actions: {
      typeOf: (action) => action.type || 'unknown',
      context: (items) => new Map(items.map((action) => [action.id, action])),
      row: actionRow,
      total: () => state.run.actionCount || 0,
      error: 'Could not load actions: {error}',
      loading: 'Loading actions…',
      empty: (stream) => (stream.items.length ? 'No loaded actions match this filter.' : 'No actions were recorded for this run.')
    },
    changes: {
      typeOf: changeFamily,
      row: changeRow,
      total: (stream) => stream.total,
      error: 'Could not load repository changes: {error}',
      loading: 'Loading repository changes…',
      empty: (stream) => (stream.status === 'unavailable' ? '' : (stream.total === 0 ? 'No repository changes were observed.' : 'No loaded changes match this filter.'))
    },
    events: {
      typeOf: eventType,
      row: eventRow,
      total: () => state.run.eventCount || 0,
      error: 'Could not load provider events: {error}',
      loading: 'Loading provider events…',
      empty: (stream) => (stream.items.length ? 'No loaded provider events match this filter.' : 'This run has no sanitized provider-event artifact.')
    }
  };

  function renderTimeline() {
    if (!state.run) return;
    const timeline = $('timeline');
    timeline.replaceChildren();
    timeline.scrollTop = 0;
    state.selected = null;
    renderInspector();
    const streamName = state.mode;
    if (streamName === 'changes' && isLive()) {
      renderLiveChanges();
      return;
    }
    const stream = state.streams[streamName];
    stream.shown = 0;
    if (stream.error) timeline.append(node('div', 'timeline-empty stream-error', t(MODES[streamName].error, { error: stream.error })));
    if (streamName === 'changes') {
      if (stream.loaded && stream.status === 'unavailable') {
        const pending = fieldsMap(state.run.evidence.repository).get('Status') === 'PENDING';
        const reason = pending ? t(REPOSITORY_PENDING) : sentence(stream.reason);
        timeline.append(node('div', 'timeline-empty change-evidence-warning', `${t('Repository change evidence is unavailable.')}${reason ? ` ${reason}` : ''}`));
      }
      const source = stream.attribution || 'observed during run, not causal proof';
      if (stream.items.length) timeline.append(labelled('div', 'timeline-note', humanAttribution(source), source));
    }
    renderTypeFilters(stream.items, MODES[streamName].typeOf);
    renderRows(timeline, streamName, 0);
    renderEmpty(timeline, streamName);
    renderTail(streamName);
  }

  function selectItem(row, selected) {
    document.querySelectorAll('.action-row.selected').forEach((el) => {
      el.classList.remove('selected');
    });
    row.classList.add('selected');
    state.selected = selected;
    renderInspector();
    const label = selected.kind === 'change' || selected.kind === 'live' ? selected.value.path : (selected.kind === 'event' ? eventType(selected.value) : (selected.value.type || selected.kind));
    announceInspector(t('{kind} selected: {label}', { kind: selected.kind === 'live' ? 'change' : selected.kind, label }));
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
    const title = kind === 'action' ? value.type : (kind === 'change' || kind === 'live' ? value.path : (value.type || value.hook_event_name || t('(untyped event)')));
    holder.append(node('div', 'inspector-title', title));
    const meta = node('div', 'inspector-meta');
    if (kind === 'action') {
      if (value.provider) meta.append(node('span', 'pill', value.provider));
      if (value.assurance) meta.append(labelled('span', 'pill', humanAttribution(value.assurance, value.provider), value.assurance));
      if (value.status) meta.append(labelled('span', 'pill', t(value.status), value.status));
      if (value.id) meta.append(node('span', 'pill', value.id));
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
    } else if (kind === 'live') {
      meta.append(node('span', 'pill', value.status), labelled('span', 'pill', t('Working tree'), live.changes ? live.changes.note : ''));
      holder.append(meta);
      addPayload(holder, 'WORKING TREE STATUS', { path: value.path, status: value.status, measuredAt: live.changes ? live.changes.measuredAt : undefined });
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
    if (section === 'Process result' && name === 'Status' && /^(UNAVAILABLE|NOT OBSERVED) \(interactive session/.test(value)) {
      const ended = fields.get('Exit Reason') === 'session_ended';
      return { text: statusWord('NOT OBSERVED'), caption: ended ? `${t(NOT_OBSERVED)} ${t(ENDED_BY_HOOK)}` : t(NOT_OBSERVED), title: value };
    }
    if (section === 'Repository delta' && name === 'Status') {
      const token = repositoryWord(value);
      if (token === 'PENDING') return { text: statusWord(token), caption: t(REPOSITORY_PENDING), title: value };
      if (token === 'NOT RECORDED') return { text: statusWord(token), caption: sentence(fields.get('Reason')) || t(REPOSITORY_NONE), title: value };
    }
    if (section === 'Verification' && name === 'Status' && VERDICTS[verdictWord(value)]) {
      const token = verdictWord(value);
      return { text: statusWord(token), caption: sentence(fields.get('Reason')) || t(VERDICTS[token]), title: value };
    }
    // A check line starts with its verdict ("PASS go test …"); only that word is translated.
    const check = name === 'Check' ? /^([A-Z]+) /.exec(value) : null;
    if (check && statusWord(check[1]) !== check[1]) return { text: statusWord(check[1]) + value.slice(check[1].length), title: value };
    if (name === 'Status') return { text: statusWord(value), title: value };
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
        const empty = fieldValue({ text: EMPTY_WORD[title] ? statusWord(EMPTY_WORD[title]) : t('Unavailable'), title: EMPTY_WORD[title], caption: t(EMPTY_SECTION[title]) });
        empty.classList.add('field-span');
        grid.append(empty);
      } else {
        const map = fieldsMap(fields);
        for (const field of fields) grid.append(labelled('span', 'field-name', t(field.name), field.name), fieldValue(describeField(title, field.name, String(field.value), map, provider)));
      }
      block.append(grid);
      if (title === 'Verification') renderPosthoc(block);
      holder.append(block);
    }
  }

  // tone is one of '', 'pass', 'fail', 'warn'; NOT RUN and RUNNING stay neutral on purpose.
  function metric(label, value, detail = '', tone = '') {
    const card = node('div', `metric${tone ? ` ${tone}` : ''}`);
    card.append(node('div', 'metric-label', t(label)), statusNode('div', 'metric-value', String(value)));
    if (detail) card.append(node('div', 'metric-detail', detail));
    return card;
  }

  // renderRunHeader draws everything above the timeline from state.run; a live tick redraws it without touching the timeline.
  function renderRunHeader() {
    const data = state.run;
    const run = data.run;
    $('run-provider').textContent = run.provider || t('unknown');
    $('run-project').textContent = run.project || t('unknown');
    $('run-title').textContent = run.id;
    $('run-subtitle').textContent = `${run.cwd || t('unknown cwd')} · ${new Date(run.startedAt).toLocaleString(state.lang)}`;
    $('run-prompt').textContent = run.prompt || t('No recorded request.');
    $('action-count').textContent = String(data.actionCount || 0);
    // A language switch re-renders the run without refetching its streams:
    // a change count already loaded is kept, not zeroed.
    const loadedChanges = state.streams && state.streams.changes && state.streams.changes.loaded ? state.streams.changes : null;
    $('change-count').textContent = isLive() && live.changes ? String((live.changes.files || []).length) : (loadedChanges ? (loadedChanges.status === 'unavailable' ? (loadedChanges.total === 0 ? '?' : `${loadedChanges.total}+?`) : String(loadedChanges.total)) : '0');
    $('event-count').textContent = String(data.eventCount || 0);
    $('top-meta').textContent = `${run.provider || t('unknown')} · ${run.exitReason || 'running'} · ${run.id}`;
    document.title = `${run.project || run.id} · agentrec`;

    const supervisor = fieldsMap(data.evidence.supervisor);
    const process = outcome(run.exitReason, supervisor);
    const verification = verificationSummary(data.evidence.verification);
    const runStatus = run.statusClass || '';
    const verdict = $('run-verdict');
    verdict.textContent = t('Run {run} · Verify {verify}', { run: statusWord(process.value), verify: statusWord(verification.value) });
    const tokens = `Run ${process.value} · Verify ${verification.value}`;
    verdict.title = [verdict.textContent === tokens ? '' : tokens, `${process.detail} ${verification.detail}`.trim()].filter(Boolean).join('\n');
    verdict.className = `verdict ${runStatus}`;
    renderRunActions();
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
    renderLivePill();
  }

  function renderRun() {
    renderRunHeader();
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

  // loadStreamPage fetches one page: append=false starts the stream over from cursor, append=true adds the page to what is loaded.
  // focusNew moves focus onto the first appended row, for a "Load more" button that is gone once its page lands.
  async function loadStreamPage(streamName, cursor = 0, append = false, generation = state.loadGeneration, focusNew = false, signal) {
    if (generation !== state.loadGeneration) return;
    const stream = state.streams && state.streams[streamName];
    if (!stream || stream.loading || cursor === null) return;
    stream.loading = true;
    stream.currentCursor = cursor;
    const from = append ? stream.items.length : 0;
    if (streamName === state.mode) renderTail(streamName);
    try {
      const page = await getJSON(`/api/snapshots/${encodeURIComponent(state.run.snapshotId)}/${streamName}?cursor=${cursor}`, signal);
      // A page for a cursor this stream no longer waits on is stale and dropped.
      if (generation !== state.loadGeneration || cursor !== stream.currentCursor) return;
      stream.items = append ? stream.items.concat(page.items || []) : (page.items || []);
      stream.error = '';
      stream.nextCursor = page.nextCursor === undefined ? null : page.nextCursor;
      if (page.endCursor !== undefined) stream.endCursor = page.endCursor;
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
      if (generation === state.loadGeneration && streamName === state.mode) {
        if (append && from > 0) {
          appendTimeline(streamName, from, focusNew);
        } else {
          renderTimeline();
          const first = focusNew ? $('timeline').querySelector('.action-row') : null;
          if (first) first.focus();
        }
      }
    }
  }

  // ── Delete a run ──────────────────────────────────────────────────────────
  // A deleted run goes to the trash (agentrec trash lists, restores and empties it); the toast's Undo restores it from here.
  // The token is fetched once; a failed fetch leaves it empty so the next mutation tries again, and a 403 drops it the same way.
  async function mutate(method, path, body) {
    if (!state.token) state.token = (await getJSON('/api/token')).token || '';
    const headers = { Accept: 'application/json', 'X-Agentrec-Token': state.token };
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    const response = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
    if (response.status === 204) return undefined;
    if (response.status === 403) state.token = '';
    const parsed = await response.json().catch(() => ({}));
    if (response.ok) return parsed;
    const error = new Error(parsed.error || `HTTP ${response.status}`);
    error.status = response.status;
    throw error;
  }

  function showToast(message, actionLabel, action) {
    const toast = $('toast');
    toast.replaceChildren(node('span', '', message));
    window.clearTimeout(state.toastTimer);
    const button = node('button', 'toast-action', actionLabel);
    button.type = 'button';
    button.addEventListener('click', () => {
      toast.classList.add('hidden');
      action();
    });
    toast.append(node('span', 'toast-sep', '·'), button);
    toast.classList.remove('hidden');
    state.toastTimer = window.setTimeout(() => toast.classList.add('hidden'), 10000);
    return button;
  }

  // The control is one button until it is activated; then the same spot holds the question and its two answers.
  function renderRunActions() {
    const holder = $('run-actions');
    holder.replaceChildren();
    if (!state.run) return;
    const run = state.run.run;
    if (state.confirmDelete) {
      const yes = node('button', 'danger-button', t('Delete'));
      yes.type = 'button';
      yes.addEventListener('click', () => deleteRun(run.id));
      const no = node('button', 'load-more', t('Cancel'));
      no.type = 'button';
      no.addEventListener('click', () => {
        state.confirmDelete = false;
        renderRunActions();
        $('delete-run').focus();
      });
      holder.append(node('span', 'confirm-text', t('Delete this run?')), yes, no);
      yes.focus();
      return;
    }
    const button = node('button', 'danger-button');
    button.type = 'button';
    button.id = 'delete-run';
    button.append($('trash-icon').content.firstElementChild.cloneNode(true), node('span', '', t('Delete run')));
    const open = !run.exitReason || run.exitReason === 'running';
    button.disabled = open;
    button.title = t(open ? 'This run is still open; it can be deleted after the session ends.' : 'Move this run to the trash');
    button.addEventListener('click', () => {
      state.confirmDelete = true;
      renderRunActions();
    });
    const compareWith = node('button', 'load-more', t('Compare with…'));
    compareWith.type = 'button';
    compareWith.id = 'compare-with';
    compareWith.addEventListener('click', () => openDiff());
    holder.append(compareWith, button);
  }

  async function deleteRun(id) {
    try {
      await mutate('DELETE', `/api/runs/${encodeURIComponent(id)}`);
    } catch (error) {
      showError(t('Cannot delete: {error}', { error: error instanceof Error ? error.message : String(error) }));
      return;
    }
    state.confirmDelete = false;
    const index = state.runs.findIndex((run) => run.id === id);
    const summary = state.runs[index];
    state.runs = state.runs.filter((run) => run.id !== id);
    const next = state.runs[Math.min(Math.max(index, 0), state.runs.length - 1)];
    if (state.run && state.run.run.id === id) {
      state.run = null;
      state.streams = null;
      state.selected = null;
    }
    renderRunList();
    renderWorkspaceState();
    if (next) loadRun(next.id);
    showToast(t('Run deleted'), t('Undo'), () => restoreRun(id, summary, index)).focus();
  }

  async function restoreRun(id, summary, index) {
    try {
      await mutate('POST', `/api/runs/${encodeURIComponent(id)}/restore`);
    } catch (error) {
      showError(t('Cannot restore: {error}', { error: error instanceof Error ? error.message : String(error) }));
      return;
    }
    if (summary && !state.runs.some((run) => run.id === id)) state.runs.splice(Math.min(Math.max(index, 0), state.runs.length), 0, summary);
    renderRunList();
    loadRun(id);
  }

  // ── Compare runners ───────────────────────────────────────────────────────
  // `agentrec shadow run` from the viewer: the form posts a job, the panel polls it once a second and appends its output.
  // Only the last job is kept, in memory; reopening the panel (or reloading) adopts the newest job the server lists.
  const COMPARE_LOG_LIMIT = 1024 * 1024;
  const compare = { info: null, job: null, poll: null, cwdTouched: false, returnFocus: null };
  const errorText = (error) => (error instanceof Error ? error.message : String(error));

  function compareRunners() {
    return Array.from($('compare-runners').querySelectorAll('input:checked')).map((box) => box.value);
  }

  function renderCompareCommand() {
    const names = compareRunners();
    $('compare-command').textContent = ['agentrec shadow run task.md', ...names.map((name) => `--runner ${name}`)].join(' ');
    // shadow run compares, so it needs every runner the server knows; a partial set only shapes the copyable command.
    const all = ((compare.info && compare.info.runners) || []).every((runner) => names.includes(runner.name));
    $('compare-runner-hint').classList.toggle('hidden', all);
    const running = Boolean(compare.job && compare.job.status === 'running');
    $('compare-run').disabled = !(compare.info && compare.info.allowRun) || running || !all;
  }

  function showCompareError(message) {
    const el = $('compare-error');
    el.textContent = message;
    el.classList.remove('hidden');
  }

  function renderCompareForm() {
    if (!compare.info) return;
    const holder = $('compare-runners');
    const runners = compare.info.runners || [];
    const checked = new Set(holder.childElementCount ? compareRunners() : runners.filter((runner) => runner.available).map((runner) => runner.name));
    holder.replaceChildren();
    for (const runner of runners) {
      const label = node('label', 'compare-runner');
      const box = node('input');
      box.type = 'checkbox';
      box.value = runner.name;
      box.checked = checked.has(runner.name);
      box.addEventListener('change', renderCompareCommand);
      label.append(box, node('span', '', runner.name));
      if (!runner.available) label.append(node('span', 'hint', t('unavailable')));
      holder.append(label);
    }
    const note = $('compare-allow-note');
    note.textContent = t('Start the viewer with `agentrec start --allow-run` to run comparisons from here');
    note.classList.toggle('hidden', Boolean(compare.info.allowRun));
    $('compare-cancel').classList.toggle('hidden', !(compare.job && compare.job.status === 'running'));
    renderCompareCommand();
  }

  function renderCompareJob() {
    const job = compare.job;
    $('compare-job').classList.toggle('hidden', !job);
    if (!job) return;
    const pill = $('compare-status');
    pill.className = `mini-status ${job.status === 'completed' ? 'pass' : (job.status === 'failed' ? 'fail' : '')}`;
    pill.textContent = t(job.status);
    pill.title = job.status;
    const seconds = Math.max(0, Math.round(((job.endedAt ? new Date(job.endedAt) : new Date()) - new Date(job.startedAt)) / 1000));
    $('compare-elapsed').textContent = Number.isFinite(seconds) ? t('Elapsed {time}', { time: `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}` }) : '';
    $('compare-exit').textContent = typeof job.exitCode === 'number' ? t('exit code {n}', { n: job.exitCode }) : '';
    const notes = [];
    if (job.dropped) notes.push(t('Older output was dropped to keep the log under 1 MiB.'));
    if (job.truncated) notes.push(t('The server truncated the output.'));
    const note = $('compare-log-note');
    note.textContent = notes.join(' ');
    note.classList.toggle('hidden', notes.length === 0);
    const links = $('compare-links');
    links.replaceChildren();
    for (const id of job.runIds || []) {
      const link = node('button', 'show-more', t('Open run {id}', { id: shortID(id) }));
      link.type = 'button';
      link.title = id;
      link.addEventListener('click', () => {
        closeCompare();
        loadRun(id);
      });
      links.append(link);
    }
  }

  // The log is one string bounded to ~1 MiB: over the bound the oldest whole lines go and the note says so.
  function appendCompareLog(chunk) {
    const job = compare.job;
    job.log += chunk;
    if (job.log.length > COMPARE_LOG_LIMIT) {
      const cut = job.log.indexOf('\n', job.log.length - COMPARE_LOG_LIMIT);
      job.log = job.log.slice(cut === -1 ? job.log.length - COMPARE_LOG_LIMIT : cut + 1);
      job.dropped = true;
    }
    const pre = $('compare-log');
    const stick = pre.scrollHeight - pre.scrollTop - pre.clientHeight < 12;
    pre.textContent = job.log;
    if (stick) pre.scrollTop = pre.scrollHeight;
  }

  function stopComparePolling() {
    window.clearTimeout(compare.poll);
    compare.poll = null;
  }

  async function pollCompareJob() {
    const job = compare.job;
    stopComparePolling();
    if (!job) return;
    try {
      const page = await getJSON(`/api/shadow/jobs/${encodeURIComponent(job.id)}?since=${job.offset}`);
      if (compare.job !== job) return;
      Object.assign(job, { status: page.status, startedAt: page.startedAt, endedAt: page.endedAt, exitCode: page.exitCode, runIds: page.runIds || [], offset: page.offset, truncated: job.truncated || Boolean(page.truncated), errors: 0 });
      if (page.chunk) appendCompareLog(page.chunk);
      renderCompareJob();
      if (job.status === 'running') {
        compare.poll = window.setTimeout(pollCompareJob, 1000);
        return;
      }
      renderCompareForm();
      refreshRuns();
    } catch (error) {
      if (compare.job !== job || error.name === 'AbortError') return;
      showCompareError(t('Could not load comparison status: {error}', { error: errorText(error) }));
      // ponytail: a few retries cover a viewer restart; a job that stays unreachable is picked up again on the next open.
      job.errors = (job.errors || 0) + 1;
      if (job.errors < 5) compare.poll = window.setTimeout(pollCompareJob, 3000);
    }
  }

  function adoptCompareJob(job) {
    compare.job = Object.assign({}, job, { log: '', offset: 0, dropped: false, truncated: false, errors: 0 });
    $('compare-log').textContent = '';
    renderCompareJob();
    pollCompareJob();
  }

  async function loadShadow() {
    try {
      compare.info = await getJSON('/api/shadow');
    } catch (error) {
      // What the viewer allows is not known from a failed request: the last
      // answer stands, so a moment's trouble does not hide the controls.
      compare.info = { allowRun: state.allowRun, runners: [{ name: 'claude', available: true }, { name: 'codex', available: true }], jobs: [] };
      showCompareError(t('Could not load comparison status: {error}', { error: errorText(error) }));
    }
    setAllowRun(compare.info.allowRun);
    const latest = (compare.info.jobs || [])[0];
    if (latest && (!compare.job || compare.job.id !== latest.id)) adoptCompareJob(latest);
    else if (compare.job && compare.job.status === 'running' && !compare.poll) pollCompareJob();
    renderCompareForm();
    renderCompareJob();
  }

  async function runCompare() {
    $('compare-error').classList.add('hidden');
    const body = { cwd: $('compare-cwd').value.trim(), task: $('compare-task').value, runners: compareRunners() };
    $('compare-run').disabled = true;
    try {
      const { id } = await mutate('POST', '/api/shadow/jobs', body);
      adoptCompareJob({ id, status: 'running', cwd: body.cwd, runners: body.runners, startedAt: new Date().toISOString(), runIds: [] });
      renderCompareForm();
    } catch (error) {
      showCompareError(t('Cannot run comparison: {error}', { error: errorText(error) }));
      // 403 means the viewer is not allowed to run, 409 that a job is already running: re-read the overview so the panel shows which.
      if (error.status === 403 || error.status === 409) loadShadow(); else renderCompareForm();
    }
  }

  async function cancelCompare() {
    if (!compare.job) return;
    $('compare-error').classList.add('hidden');
    try {
      await mutate('POST', `/api/shadow/jobs/${encodeURIComponent(compare.job.id)}/cancel`);
    } catch (error) {
      showCompareError(t('Cannot cancel: {error}', { error: errorText(error) }));
    }
  }

  async function copyCompareCommand() {
    const code = $('compare-command');
    try {
      await navigator.clipboard.writeText(code.textContent);
      $('compare-copy').textContent = t('Copied');
      window.setTimeout(() => { $('compare-copy').textContent = t('Copy'); }, 1500);
    } catch (error) {
      const range = document.createRange();
      range.selectNodeContents(code);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
    }
  }

  function openCompare() {
    compare.returnFocus = document.activeElement;
    const cwd = $('compare-cwd');
    if (!compare.cwdTouched && state.run && state.run.run.cwd) cwd.value = state.run.run.cwd;
    $('compare-backdrop').classList.remove('hidden');
    $('compare-panel').classList.remove('hidden');
    loadShadow();
    cwd.focus();
  }

  function closeCompare() {
    $('compare-backdrop').classList.add('hidden');
    $('compare-panel').classList.add('hidden');
    if (compare.returnFocus && compare.returnFocus.focus) compare.returnFocus.focus();
  }

  $('compare-open').addEventListener('click', openCompare);
  $('compare-close').addEventListener('click', closeCompare);
  $('compare-backdrop').addEventListener('click', closeCompare);
  $('compare-run').addEventListener('click', runCompare);
  $('compare-cancel').addEventListener('click', cancelCompare);
  $('compare-copy').addEventListener('click', copyCompareCommand);
  $('compare-cwd').addEventListener('input', () => { compare.cwdTouched = true; });
  // Escape closes; Tab stays inside the sheet while it is open.
  function sheetKeys(panel, close) {
    panel.addEventListener('keydown', (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        close();
        return;
      }
      if (event.key !== 'Tab') return;
      const items = Array.from(panel.querySelectorAll('button:not(:disabled), input:not(:disabled), textarea, [tabindex="0"]')).filter((el) => el.offsetParent !== null);
      if (items.length === 0) return;
      const first = items[0];
      const last = items[items.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    });
  }
  sheetKeys($('compare-panel'), closeCompare);

  // setAllowRun records whether the viewer may run things (agentrec start --allow-run); the Verify now button follows it.
  function setAllowRun(allowed) {
    const next = Boolean(allowed);
    if (next === state.allowRun) return;
    state.allowRun = next;
    if (state.run) renderEvidence();
  }

  // ── Verify later ──────────────────────────────────────────────────────────
  // POST /api/runs/{id}/verify runs the repository's committed checks now, against the repository as it is now. The result
  // reaches the page as evidence.posthocVerification and is drawn under the run's own verification, never in its place.
  const verify = { runId: '', busy: false, error: '' };
  const verdictOf = (status) => {
    const raw = String(status || '');
    const lower = raw.toLowerCase();
    return lower === 'passed' ? 'PASS' : (lower === 'failed' ? 'FAIL' : raw.toUpperCase());
  };
  const quoteArgv = (argv) => (Array.isArray(argv) ? argv : []).map((arg) => JSON.stringify(String(arg))).join(' ');

  // checkSummary spells one check the way the server's Check field does, so describeField draws both alike.
  function checkSummary(check) {
    const parts = [`${check.status ? verdictOf(check.status) : 'PENDING'} ${check.name || ''}`, quoteArgv(check.command)];
    if (check.durationMs > 0) parts.push(check.durationMs < 1000 ? `${check.durationMs}ms` : `${(check.durationMs / 1000).toFixed(1)}s`);
    if (typeof check.exitCode === 'number') parts.push(`exit ${check.exitCode}`);
    if (check.signal) parts.push(`signal ${check.signal}`);
    return parts.join('  ');
  }

  function posthocFields(doc) {
    const fields = [{ name: 'Status', value: verdictOf(doc.status) }];
    if (doc.reason) fields.push({ name: 'Reason', value: doc.reason });
    for (const check of doc.checks || []) fields.push({ name: 'Check', value: checkSummary(check) });
    return fields;
  }

  // renderPosthoc adds the Verify now control and, when a later verification exists, its own sub-section to the Verification block.
  function renderPosthoc(block) {
    const data = state.run;
    const id = data.run.id;
    const doc = data.evidence.posthocVerification;
    const busy = verify.busy && verify.runId === id;
    if (state.allowRun && !isLive()) {
      const actions = node('div', 'verify-actions');
      const button = node('button', 'load-more', t(busy ? 'Verifying…' : 'Verify now'));
      button.type = 'button';
      button.id = 'verify-now';
      button.disabled = verify.busy;
      button.title = t('Runs the checks committed in the repository now, in the run\'s repository.');
      button.setAttribute('aria-busy', String(busy));
      button.addEventListener('click', () => verifyRun(id));
      actions.append(button);
      block.append(actions);
    }
    if (verify.error && verify.runId === id) block.append(node('p', 'compare-error verify-error', verify.error));
    if (!doc) return;
    // A later verdict must not stand in for the run's own: when the run was never
    // verified at its end, the block says so before the later section is drawn.
    if (doc.ownRan === false) block.append(node('p', 'posthoc-caveat', t('This run was not verified when it ended.')));
    const sub = node('section', 'posthoc');
    const heading = node('div', 'evidence-title');
    heading.append(node('span', '', t('Verified later')), node('span', 'evidence-source', t('Measured at {time}', { time: doc.measuredAt ? new Date(doc.measuredAt).toLocaleString(state.lang) : '—' })));
    // The page-authored caveat is shown; the server's own sentence, when it sends one, stays in the title.
    // Whether HEAD moved has three answers: moved, did not move, and not
    // known. Saying nothing in the last case would read as "it did not move".
    const caveatKey = doc.headMovedSince === true
      ? 'Run later, against the repository as it is now — not the state the run left behind; the repository HEAD has moved since.'
      : doc.headMovedSince === false
        ? 'Run later, against the repository as it is now — not the state the run left behind; the repository HEAD has not moved since.'
        : 'Run later, against the repository as it is now — not the state the run left behind; whether the repository HEAD moved since is not known.';
    const caveat = labelled('p', 'posthoc-caveat', t(caveatKey), doc.caveat);
    const grid = node('div', 'evidence-fields');
    // The server sends the rows ready-made (fields, like evidence.verification); a bare document with checks[] is spelled here.
    const fields = Array.isArray(doc.fields) ? doc.fields : posthocFields(doc);
    const map = fieldsMap(fields);
    for (const field of fields) grid.append(labelled('span', 'field-name', t(field.name), field.name), fieldValue(describeField('Verification', field.name, String(field.value), map, data.run.provider)));
    sub.append(heading, caveat, grid);
    for (const check of doc.checks || []) {
      if (!check.stdout && !check.stderr) continue;
      const details = node('details', 'posthoc-output');
      details.append(node('summary', '', check.name || ''));
      addPayload(details, 'STDOUT', check.stdout);
      addPayload(details, 'STDERR', check.stderr);
      sub.append(details);
    }
    block.append(sub);
  }

  async function verifyRun(id) {
    if (verify.busy) return;
    Object.assign(verify, { runId: id, busy: true, error: '' });
    renderEvidence();
    try {
      const result = await mutate('POST', `/api/runs/${encodeURIComponent(id)}/verify`);
      const fresh = await getJSONRetrying(`/api/runs/${encodeURIComponent(id)}`);
      // ponytail: the snapshot carries the document; if it does not yet, the reply (the document itself, or wrapped) stands in.
      const doc = result && Array.isArray(result.fields) ? result : (result && result.verification ? Object.assign({ measuredAt: result.measuredAt, headMovedSince: null }, result.verification) : null);
      if (doc && !fresh.evidence.posthocVerification) fresh.evidence.posthocVerification = doc;
      if (state.run && state.run.run.id === id) {
        state.run = fresh;
        live.signature = runSignature(fresh);
        renderRunHeader();
      }
    } catch (error) {
      verify.error = t('Cannot verify: {error}', { error: errorText(error) });
    } finally {
      verify.busy = false;
      if (state.run && state.run.run.id === id) renderEvidence();
    }
  }

  // ── Compare two runs ──────────────────────────────────────────────────────
  // Client-side only: both snapshots come from GET /api/runs/{id}; their change lists and action types are paged to the end
  // from the snapshot streams. The sheet is the compare-runners one with a run picker where the form would be.
  const DIFF_ACTION_PAGES = 40;
  const DIFF_CHANGE_PAGES = 40;
  const DIFF_FILE_CAP = 300;
  const diff = { a: '', b: '', bundles: null, generation: 0, returnFocus: null };

  async function pageAll(path, maxPages = Infinity) {
    const items = [];
    let page = null;
    let cursor = 0;
    for (let n = 0; n < maxPages && cursor !== null; n += 1) {
      page = await getJSON(`${path}?cursor=${cursor}`);
      items.push(...(page.items || []));
      cursor = page.nextCursor === undefined ? null : page.nextCursor;
    }
    return { items, page, truncated: cursor !== null };
  }

  async function loadRunBundle(id) {
    const run = await getJSONRetrying(`/api/runs/${encodeURIComponent(id)}`);
    const base = `/api/snapshots/${encodeURIComponent(run.snapshotId)}`;
    const [changes, actions] = await Promise.all([pageAll(`${base}/changes`, DIFF_CHANGE_PAGES), run.actionCount ? pageAll(`${base}/actions`, DIFF_ACTION_PAGES) : { items: [], truncated: false }]);
    const types = new Map();
    for (const action of actions.items) {
      const type = action.type || 'unknown';
      types.set(type, (types.get(type) || 0) + 1);
    }
    return { run, changes: changes.items, changesTruncated: changes.truncated, types, actionsCounted: actions.items.length, actionsTruncated: actions.truncated };
  }

  const num = (value) => (value === undefined || value === null || value === '' || Number.isNaN(Number(value)) ? '' : Number(value).toLocaleString(state.lang));

  // bundleFacts is the comparison's row set, in row order: a string, a node, or '' when the run has no such fact.
  function bundleFacts(bundle) {
    const data = bundle.run;
    const run = data.run;
    const usage = fieldsMap(data.evidence.providerUsage);
    const supervisor = fieldsMap(data.evidence.supervisor);
    const process = outcome(run.exitReason, supervisor);
    const verification = verificationSummary(data.evidence.verification);
    const changes = data.changes || {};
    const repoStatus = String(changes.status || 'unavailable').toUpperCase();
    const repoWord = repoStatus === 'AVAILABLE' ? 'AVAILABLE' : (repoStatus === 'PENDING' || fieldsMap(data.evidence.repository).get('Status') === 'PENDING' ? 'PENDING' : 'NOT RECORDED');
    const top = [...bundle.types.entries()].sort((x, y) => y[1] - x[1] || x[0].localeCompare(y[0])).slice(0, 6);
    const types = node('div', 'diff-types');
    for (const [type, count] of top) types.append(node('div', '', `${t(TYPE_LABELS[type] || type)} ${count}`));
    if (bundle.actionsTruncated) types.append(node('div', 'diff-note', t('counted from the first {n} actions', { n: bundle.actionsCounted })));
    const pill = (token, tone) => statusNode('span', `mini-status${tone ? ` ${tone}` : ''}`, token);
    return {
      Provider: run.provider || '',
      Model: usage.get('Model') || '',
      'Provider version': run.providerVersion || '',
      Started: run.startedAt ? new Date(run.startedAt).toLocaleString(state.lang) : '',
      Duration: supervisor.has('Duration') ? t(supervisor.get('Duration')) : duration({ startedAt: run.startedAt || '', finishedAt: run.endedAt || '' }),
      'Exit Reason': pill(process.value, process.tone),
      'Input Tokens': num(usage.get('Input Tokens')),
      'Cached Input Tokens': num(usage.get('Cached Input Tokens')),
      'Cache Creation Input Tokens': num(usage.get('Cache Creation Input Tokens')),
      'Output Tokens': num(usage.get('Output Tokens')),
      'Cost USD': usage.get('Cost USD') || '',
      Actions: num(data.actionCount || 0),
      'Provider events': num(data.eventCount || 0),
      'Actions by type': top.length ? types : '',
      'Files changed': repoStatus === 'AVAILABLE' ? num(changes.total || 0) : '',
      Verification: pill(verification.value, verification.tone),
      'Repository status': pill(repoWord, '')
    };
  }

  function diffHeader(run, current) {
    const th = node('th', '', shortID(run.id));
    th.scope = 'col';
    th.title = run.id;
    if (current) th.append(node('span', 'diff-current', t('This run')));
    return th;
  }

  // diffFiles is the three-column file list: only in A, only in B, in both (with both kinds when they differ).
  function diffFiles(a, b) {
    const kindOf = (change) => change.kind || changeFamily(change);
    const mapA = new Map(a.changes.map((change) => [change.path, change]));
    const mapB = new Map(b.changes.map((change) => [change.path, change]));
    const only = (from, other) => [...from.values()].filter((change) => !other.has(change.path)).map((change) => [change, null]);
    const both = [...mapA.values()].filter((change) => mapB.has(change.path)).map((change) => [change, mapB.get(change.path)]);
    const column = (title, entries) => {
      const col = node('div', 'diff-col');
      col.append(node('div', 'diff-col-head', `${title} · ${entries.length}`));
      const list = node('ul');
      for (const [x, y] of entries.slice(0, DIFF_FILE_CAP)) {
        const li = node('li', '', x.path);
        const kinds = [kindOf(x)];
        if (y && kindOf(y) !== kinds[0]) kinds.push(kindOf(y));
        li.append(node('span', 'diff-kind', kinds.map((kind) => t(kind)).join(' / ')));
        list.append(li);
      }
      if (entries.length > DIFF_FILE_CAP) list.append(node('li', 'diff-note', t('+{n} more', { n: entries.length - DIFF_FILE_CAP })));
      if (entries.length === 0) list.append(node('li', 'diff-note', t('No files')));
      col.append(list);
      return col;
    };
    const files = node('div', 'diff-files');
    files.append(column(t('Only in {id}', { id: shortID(a.run.run.id) }), only(mapA, mapB)), column(t('Only in {id}', { id: shortID(b.run.run.id) }), only(mapB, mapA)), column(t('In both'), both));
    // A change list that was cut short is not a full answer, and the split
    // into only-here and only-there would read as one.
    if (a.changesTruncated || b.changesTruncated) files.append(node('p', 'diff-note', t('Some changed files were not read; this split is incomplete.')));
    return files;
  }

  function renderDiff(a, b) {
    const holder = $('diff-result');
    holder.replaceChildren();
    const again = node('button', 'load-more', t('Pick another run'));
    again.type = 'button';
    again.addEventListener('click', showDiffPicker);
    const actions = node('div', 'diff-actions');
    actions.append(again);
    const factsA = bundleFacts(a);
    const factsB = bundleFacts(b);
    const table = node('table', 'diff-table');
    const thead = node('thead');
    const head = node('tr');
    head.append(node('th', '', ''), diffHeader(a.run.run, true), diffHeader(b.run.run, false));
    thead.append(head);
    const tbody = node('tbody');
    for (const label of Object.keys(factsA)) {
      const valueA = factsA[label];
      const valueB = factsB[label];
      if (valueA === '' && valueB === '') continue;
      const row = node('tr');
      const cellA = node('td');
      const cellB = node('td');
      cellA.append(valueA === '' ? '—' : valueA);
      cellB.append(valueB === '' ? '—' : valueB);
      if (cellA.textContent !== cellB.textContent) row.classList.add('differs');
      const name = labelled('th', '', t(label), label);
      name.scope = 'row';
      row.append(name, cellA, cellB);
      tbody.append(row);
    }
    table.append(thead, tbody);
    holder.append(actions, table, diffFiles(a, b));
  }

  function renderDiffList() {
    const query = $('diff-search').value.trim().toLowerCase();
    const list = $('diff-list');
    list.replaceChildren();
    let shown = 0;
    for (const run of state.runs) {
      if (run.id === diff.a || !runMatches(run, query)) continue;
      shown += 1;
      const button = runItem(run, false);
      button.addEventListener('click', () => loadDiff(diff.a, run.id));
      list.append(button);
    }
    const empty = $('diff-list-empty');
    empty.textContent = t(state.runs.length < 2 ? 'No other runs to compare.' : 'No runs match this search.');
    empty.classList.toggle('hidden', shown > 0);
  }

  // The hash names an open comparison (#compare=a,b) so the page can reopen it; it is cleared when the sheet closes.
  function setDiffHash() {
    const hash = diff.a && diff.b ? `#compare=${encodeURIComponent(diff.a)},${encodeURIComponent(diff.b)}` : '';
    if (hash) {
      if (location.hash !== hash) history.replaceState(null, '', hash);
    } else if (location.hash.startsWith('#compare=')) {
      history.replaceState(null, '', location.pathname + location.search);
    }
  }

  function showDiffPicker() {
    diff.b = '';
    diff.bundles = null;
    setDiffHash();
    $('diff-result').classList.add('hidden');
    $('diff-picker').classList.remove('hidden');
    renderDiffList();
    $('diff-search').focus();
  }

  async function loadDiff(aID, bID) {
    const generation = ++diff.generation;
    diff.b = bID;
    diff.bundles = null;
    setDiffHash();
    $('diff-error').classList.add('hidden');
    $('diff-picker').classList.add('hidden');
    const result = $('diff-result');
    result.classList.remove('hidden');
    result.replaceChildren(node('div', 'diff-status', t('Loading comparison…')));
    try {
      const bundles = await Promise.all([loadRunBundle(aID), loadRunBundle(bID)]);
      if (generation !== diff.generation) return;
      diff.bundles = bundles;
      renderDiff(bundles[0], bundles[1]);
    } catch (error) {
      if (generation !== diff.generation) return;
      const el = $('diff-error');
      el.textContent = t('Could not load comparison: {error}', { error: errorText(error) });
      el.classList.remove('hidden');
      showDiffPicker();
    }
  }

  function renderDiffSheet() {
    if ($('diff-panel').classList.contains('hidden')) return;
    $('diff-intro').textContent = t('Pick another recorded run to compare with {id}.', { id: shortID(diff.a) });
    if (diff.bundles) renderDiff(diff.bundles[0], diff.bundles[1]); else if (!diff.b) renderDiffList();
  }

  function openDiff(bID = '') {
    if (!state.run) return;
    diff.returnFocus = document.activeElement;
    diff.a = state.run.run.id;
    $('diff-error').classList.add('hidden');
    $('diff-backdrop').classList.remove('hidden');
    $('diff-panel').classList.remove('hidden');
    renderDiffSheet();
    if (bID && bID !== diff.a) loadDiff(diff.a, bID); else showDiffPicker();
  }

  function closeDiff() {
    diff.generation += 1;
    diff.b = '';
    diff.bundles = null;
    setDiffHash();
    $('diff-backdrop').classList.add('hidden');
    $('diff-panel').classList.add('hidden');
    if (diff.returnFocus && diff.returnFocus.focus) diff.returnFocus.focus();
  }

  $('diff-close').addEventListener('click', closeDiff);
  $('diff-backdrop').addEventListener('click', closeDiff);
  $('diff-search').addEventListener('input', renderDiffList);
  sheetKeys($('diff-panel'), closeDiff);

  // ── Live run ──────────────────────────────────────────────────────────────
  // A run that is still recording is re-read every LIVE_MS: the header redraws when its facts changed, each fully loaded
  // stream fetches only the rows after its endCursor from the new snapshot (the files are append-only, so offsets hold),
  // and the Changes tab shows the working tree as it is now. One tick at a time; none while the tab is hidden.
  const live = { timer: null, busy: false, updatedAt: null, signature: '', changes: null, error: '' };
  const isLive = () => Boolean(state.run) && (!state.run.run.exitReason || state.run.run.exitReason === 'running');
  const runSignature = (run) => JSON.stringify([run.actionCount, run.eventCount, run.changes, run.evidence, run.run]);

  function renderLivePill() {
    $('live-pill').classList.toggle('hidden', !isLive());
    $('live-text').textContent = t('Live · updated {time}', { time: live.updatedAt ? clock(live.updatedAt.toISOString()) : '—' });
  }

  function stopLive() {
    window.clearTimeout(live.timer);
    live.timer = null;
  }

  function startLive() {
    if (live.timer || live.busy || !isLive() || document.visibilityState === 'hidden') return;
    live.timer = window.setTimeout(liveTick, LIVE_MS);
  }

  // loadTail appends the rows recorded since the stream's last page; the view follows only when it was already at the bottom.
  async function loadTail(streamName, generation) {
    const stream = state.streams && state.streams[streamName];
    if (!stream || !stream.loaded || stream.loading || stream.nextCursor !== null) return;
    const timeline = $('timeline');
    const follow = streamName === state.mode && timeline.scrollHeight - timeline.scrollTop - timeline.clientHeight < 12;
    await loadStreamPage(streamName, stream.endCursor || 0, true, generation);
    if (follow && streamName === state.mode && generation === state.loadGeneration) timeline.scrollTop = timeline.scrollHeight;
  }

  async function loadLiveChanges(generation) {
    if (!state.run) return;
    try {
      const fresh = await getJSON(`/api/runs/${encodeURIComponent(state.run.run.id)}/live`);
      if (generation !== state.loadGeneration) return;
      live.changes = fresh;
      live.error = '';
    } catch (error) {
      if (generation !== state.loadGeneration) return;
      // A 409 means the run ended between ticks; the next tick loads the measured diff instead.
      live.error = errorText(error);
    }
    $('change-count').textContent = live.changes ? String((live.changes.files || []).length) : '?';
    if (state.mode === 'changes' && isLive()) renderLiveChanges();
  }

  async function liveTick() {
    live.timer = null;
    if (live.busy || !isLive() || document.visibilityState === 'hidden') return;
    live.busy = true;
    const generation = state.loadGeneration;
    try {
      const fresh = await getJSON(`/api/runs/${encodeURIComponent(state.run.run.id)}`);
      if (generation !== state.loadGeneration) return;
      const grew = { actions: (fresh.actionCount || 0) > (state.run.actionCount || 0), events: (fresh.eventCount || 0) > (state.run.eventCount || 0) };
      const signature = runSignature(fresh);
      state.run = fresh;
      live.updatedAt = new Date();
      const running = isLive();
      if (signature !== live.signature) {
        live.signature = signature;
        renderRunHeader();
        // The visible stream's "Loaded n of total" reads the new total; pages still to come are fetched from the new snapshot.
        const label = $('timeline').querySelector('.stream-tail .pager-label');
        if (label && MODES[state.mode] && state.streams[state.mode].items.length) label.textContent = t('Loaded {loaded} of {total}', { loaded: state.streams[state.mode].items.length, total: MODES[state.mode].total(state.streams[state.mode]) });
      } else {
        renderLivePill();
      }
      for (const name of ['actions', 'events']) if (grew[name] || !running) await loadTail(name, generation);
      if (!running) {
        // The session ended: the repository diff is measured now, so the Changes tab moves from the working tree to the snapshot.
        live.changes = null;
        await loadStreamPage('changes', 0, false, generation);
        refreshRuns();
        return;
      }
      if (state.mode === 'changes') await loadLiveChanges(generation);
    } catch (error) {
      // ponytail: a failed tick stays quiet; the next one retries and user-initiated loads still surface errors.
    } finally {
      live.busy = false;
      startLive();
    }
  }

  // ── Search all runs ───────────────────────────────────────────────────────
  // The newest query wins: a keystroke restarts the idle timer and aborts the request in flight. Hits stay grouped by run
  // in the server's order (newest run first); the field keeps its text when the panel closes.
  const search = { timer: null, controller: null, hits: [], query: '', truncated: false, active: -1, open: false };

  function closeSearch() {
    window.clearTimeout(search.timer);
    search.open = false;
    search.active = -1;
    $('search-results').classList.add('hidden');
    $('search-all').setAttribute('aria-expanded', 'false');
    $('search-all').removeAttribute('aria-activedescendant');
  }

  function scheduleSearch() {
    window.clearTimeout(search.timer);
    const query = $('search-all').value.trim();
    if (query.length < 2) {
      closeSearch();
      return;
    }
    search.timer = window.setTimeout(() => runSearch(query), SEARCH_MS);
  }

  async function runSearch(query) {
    window.clearTimeout(search.timer);
    if (search.controller) search.controller.abort();
    const controller = new AbortController();
    search.controller = controller;
    search.query = query;
    renderSearch(t('Searching…'));
    try {
      const result = await getJSON(`/api/search?q=${encodeURIComponent(query)}&limit=100`, controller.signal);
      if (controller !== search.controller) return;
      search.hits = result.hits || [];
      search.truncated = Boolean(result.truncated);
      search.active = -1;
      renderSearch();
    } catch (error) {
      if (error.name === 'AbortError' || controller !== search.controller) return;
      renderSearch(t('Search failed: {error}', { error: errorText(error) }));
    } finally {
      if (controller === search.controller) search.controller = null;
    }
  }

  // highlighted renders the snippet as text with the first match wrapped in a mark; no markup comes from the payload.
  function highlighted(text, needle) {
    const holder = node('div', 'search-snippet');
    const index = needle ? text.toLowerCase().indexOf(needle.toLowerCase()) : -1;
    if (index < 0) {
      holder.textContent = text;
      return holder;
    }
    holder.append(text.slice(0, index), node('mark', 'search-match', text.slice(index, index + needle.length)), text.slice(index + needle.length));
    return holder;
  }

  function renderSearch(message) {
    const panel = $('search-results');
    const field = $('search-all');
    panel.replaceChildren();
    panel.classList.remove('hidden');
    field.setAttribute('aria-expanded', 'true');
    field.removeAttribute('aria-activedescendant');
    search.open = true;
    if (message) {
      panel.append(node('div', 'search-status', message));
      return;
    }
    const groups = new Map();
    search.hits.forEach((hit, index) => {
      if (!groups.has(hit.runId)) groups.set(hit.runId, { hit, entries: [] });
      groups.get(hit.runId).entries.push(index);
    });
    const summary = node('div', 'search-status');
    summary.append(node('span', '', search.hits.length ? t('{n} hit(s) in {m} run(s)', { n: search.hits.length, m: groups.size }) : t('No matches for this search.')));
    if (search.truncated) summary.append(node('span', 'search-truncated', t('Results truncated')));
    panel.append(summary);
    for (const { hit, entries } of groups.values()) {
      const group = node('div', 'search-group');
      const head = node('div', 'search-group-head');
      head.append(node('span', 'search-group-project', hit.project || t('unknown project')), node('span', '', relativeTime(hit.startedAt)), node('span', 'search-group-id', shortID(hit.runId)));
      head.title = hit.runId;
      group.append(head);
      for (const index of entries) {
        const entry = search.hits[index];
        const row = node('div', `search-hit${index === search.active ? ' active' : ''}`);
        row.id = `search-hit-${index}`;
        row.setAttribute('role', 'option');
        row.setAttribute('aria-selected', String(index === search.active));
        row.append(node('span', `search-kind kind-${entry.kind}`, t(entry.kind)));
        if (entry.type) row.append(node('span', 'search-type', entry.type));
        row.append(highlighted(entry.snippet || '', search.query));
        row.addEventListener('click', () => openHit(index));
        group.append(row);
      }
      panel.append(group);
    }
  }

  function moveSearchActive(delta) {
    const count = search.hits.length;
    if (count === 0) return;
    search.active = (search.active + delta + count) % count;
    const field = $('search-all');
    $('search-results').querySelectorAll('.search-hit').forEach((row) => {
      const active = row.id === `search-hit-${search.active}`;
      row.classList.toggle('active', active);
      row.setAttribute('aria-selected', String(active));
      if (active) {
        field.setAttribute('aria-activedescendant', row.id);
        row.scrollIntoView({ block: 'nearest' });
      }
    });
  }

  // openHit opens the hit's run; an action hit loads the actions page that starts at the hit's offset and selects that row.
  async function openHit(index) {
    const hit = search.hits[index];
    if (!hit) return;
    closeSearch();
    if (hit.kind === 'action') $('timeline-tab-actions').click();
    await loadRun(hit.runId, false, hit.kind === 'action' ? hit.offset || 0 : 0);
    if (hit.kind !== 'action' || !state.run || state.run.run.id !== hit.runId || state.mode !== 'actions') return;
    const items = state.streams.actions.items;
    const at = Math.max(0, items.findIndex((action) => action.id === hit.actionId));
    const row = $('timeline').querySelector(`.action-row[data-index="${at}"]`);
    if (!row || !items[at]) return;
    selectItem(row, { kind: 'action', value: items[at] });
    row.scrollIntoView({ block: 'center' });
    row.focus({ preventScroll: true });
  }

  // quiet loads (auto-selection) report failure in the empty state rather than a toast, so the poll can retry without nagging.
  // cursor starts the first actions page at a byte offset, for a search hit: the page there begins with the hit's action.
  async function loadRun(id, quiet = false, cursor = 0) {
    stopLive();
    if (state.runAbortController) state.runAbortController.abort();
    const controller = new AbortController();
    state.runAbortController = controller;
    const generation = ++state.loadGeneration;
    try {
      const run = await getJSONRetrying(`/api/runs/${encodeURIComponent(id)}`, controller.signal);
      if (generation !== state.loadGeneration) return;
      state.run = run;
      state.confirmDelete = false;
      // The comparison sheet may already be open on the run that was showing:
      // its repository path follows the run in view until someone edits it.
      if (!compare.cwdTouched && run.run.cwd) $('compare-cwd').value = run.run.cwd;
      Object.assign(live, { updatedAt: new Date(), signature: runSignature(run), changes: null, error: '' });
      // shown counts the rows the filter lets through, across every page loaded so far.
      state.streams = {
        actions: { items: [], currentCursor: 0, nextCursor: state.run.actionCount === 0 ? null : 0, endCursor: 0, loading: false, loaded: state.run.actionCount === 0, error: '', shown: 0 },
        changes: { items: [], currentCursor: 0, nextCursor: 0, loading: false, loaded: false, error: '', shown: 0, total: 0, attribution: '', baseline: '', status: '', reason: '' },
        events: { items: [], currentCursor: 0, nextCursor: state.run.eventCount === 0 ? null : 0, endCursor: 0, loading: false, loaded: state.run.eventCount === 0, error: '', shown: 0 }
      };
      state.activeTypes.clear();
      state.selected = null;
      renderRun();
      await loadStreamPage(state.mode, state.mode === 'actions' ? cursor : 0, false, generation, false, controller.signal);
      if (state.mode !== 'actions') await loadStreamPage('actions', 0, false, generation, false, controller.signal);
      if (state.mode !== 'changes') await loadStreamPage('changes', 0, false, generation, false, controller.signal);
      if (generation !== state.loadGeneration) return;
      if (isLive() && state.mode === 'changes') await loadLiveChanges(generation);
      startLive();
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

  function applyRunList(list, append = false) {
    const incoming = list.runs || [];
    const previousRuns = state.runs;
    const previousCursor = state.runNextCursor;
    const sameGeneration = (list.generation || '') === state.runGeneration;
    const pageIDs = new Set(list.pageIds || incoming.map((run) => run.id));
    const runs = append && sameGeneration
      ? [...previousRuns, ...incoming.filter((run) => !previousRuns.some((current) => current.id === run.id))]
      : (!append && sameGeneration ? [...incoming, ...previousRuns.filter((run) => !pageIDs.has(run.id))] : incoming);
    // ponytail: rebuild the list only when its content changed; a rebuild mid-click would swallow the click.
    const signature = JSON.stringify(runs.map((run) => [run.id, run.provider, run.project, run.exit, run.verification, run.statusClass, run.statusLabel, run.startedAt]));
    const changed = signature !== state.runsSignature;
    state.runsSignature = signature;
    state.runs = runs;
    state.runTotal = list.total || runs.length;
    state.runNextCursor = runs.length >= state.runTotal ? '' : (append || !sameGeneration || previousRuns.length <= incoming.length ? (list.nextCursor || '') : previousCursor);
    state.runGeneration = list.generation || '';
    const warning = $('unreadable-warning');
    if (list.unreadable) {
      warning.textContent = t('{n} unreadable run(s) were excluded.', { n: list.unreadable });
      warning.classList.remove('hidden');
    } else {
      warning.classList.add('hidden');
    }
    if (changed) {
      renderRunList();
      if (!$('diff-panel').classList.contains('hidden') && !diff.b) renderDiffList();
    } else {
      const byID = new Map(runs.map((run) => [run.id, run]));
      document.querySelectorAll('.run-item').forEach((button) => {
        const run = byID.get(button.dataset.runId);
        if (run) button.querySelector('.run-time').textContent = relativeTime(run.startedAt);
      });
    }
    renderWorkspaceState();
  }

  async function loadMoreRuns() {
    if (!state.runNextCursor) return;
    const button = $('run-load-more');
    const cursor = state.runNextCursor;
    const generation = state.runGeneration;
    button.disabled = true;
    try {
      const list = await getJSON(`/api/runs?cursor=${encodeURIComponent(cursor)}`);
      if (cursor !== state.runNextCursor || generation !== state.runGeneration || (list.generation || '') !== generation) return;
      applyRunList(list, true);
    } catch (error) {
      showError(error);
    } finally {
      button.disabled = false;
    }
  }

  // Selects the initial or newest run when nothing is shown; a failed attempt is retried quietly on the next poll.
  function autoSelect(list) {
    if (state.run || state.runAbortController) return;
    if (list.initialRunId) return loadRun(list.initialRunId, true);
    if (state.runs.length === 0) return;
    return loadRun(state.runs[0].id, true);
  }

  // Live refresh: re-fetch /api/runs and redraw the list in place. Selection, focus and the loaded run are untouched.
  async function refreshRuns() {
    if (state.pollController || document.visibilityState === 'hidden') return;
    state.pollController = new AbortController();
    try {
      const list = await getJSON('/api/runs', state.pollController.signal);
      state.storeBytes = list.storeBytes || 0;
      state.trashBytes = list.trashBytes || 0;
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
    // ponytail: the overview says whether --allow-run is on; a failure leaves the Verify now button hidden.
    getJSON('/api/shadow').then((info) => setAllowRun(info.allowRun)).catch(() => {});
    try {
      const list = await getJSON('/api/runs');
      state.storeBytes = list.storeBytes || 0;
      state.trashBytes = list.trashBytes || 0;
      applyRunList(list);
      await autoSelect(list);
      const reopen = /^#compare=([^,]+),(.+)$/.exec(location.hash);
      if (reopen) {
        const [a, b] = [decodeURIComponent(reopen[1]), decodeURIComponent(reopen[2])];
        if (!state.run || state.run.run.id !== a) await loadRun(a);
        if (state.run && state.run.run.id === a) openDiff(b);
      }
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
      stopLive();
    } else {
      refreshRuns();
      startPolling();
      startLive();
    }
  });

  // Switching language re-renders from state; the timeline selection resets, everything else stays.
  $('lang').addEventListener('change', (event) => {
    setLang(event.target.value);
    if (state.run) renderRun(); else { renderRunList(); renderWorkspaceState(); }
    renderCompareForm();
    renderCompareJob();
    renderDiffSheet();
    if (search.open) renderSearch();
  });
  $('run-search').addEventListener('input', renderRunList);
  $('run-exit-filter').addEventListener('change', renderRunList);
  $('run-verification-filter').addEventListener('change', renderRunList);
  $('run-load-more').addEventListener('click', loadMoreRuns);
  const searchAll = $('search-all');
  searchAll.addEventListener('input', scheduleSearch);
  searchAll.addEventListener('focus', () => { if (search.hits.length && searchAll.value.trim() === search.query) renderSearch(); });
  searchAll.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') {
      // Escape closes the panel and keeps the text; the native search control would clear it.
      event.preventDefault();
      closeSearch();
    } else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      if (!search.open) {
        if (!search.hits.length) return;
        renderSearch();
      }
      moveSearchActive(event.key === 'ArrowDown' ? 1 : -1);
    } else if (event.key === 'Enter') {
      event.preventDefault();
      if (search.open && search.active >= 0) openHit(search.active);
      else if (searchAll.value.trim().length >= 2) runSearch(searchAll.value.trim());
    }
  });
  document.addEventListener('click', (event) => { if (search.open && !$('global-search').contains(event.target)) closeSearch(); });
  $('run-actions').addEventListener('keydown', (event) => {
    if (event.key !== 'Escape' || !state.confirmDelete) return;
    state.confirmDelete = false;
    renderRunActions();
    $('delete-run').focus();
  });
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
      if (isLive() && state.mode === 'changes') {
        if (!live.changes) await loadLiveChanges(state.loadGeneration);
      } else if (state.streams && !state.streams[state.mode].loaded) {
        await loadStreamPage(state.mode);
      }
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
