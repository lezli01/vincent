# Changelog

All notable changes to vincent are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/) with the pre-1.0 caveat spelled out
in [Versioning and stability](#versioning-and-stability) below.

Release Please maintains release entries from Conventional Commit history. The
release pull request is the review point for improving generated entries with
the human context a commit subject cannot carry. The existing `[Unreleased]`
content predates that automation and should be reconciled in the first Release
Please pull request.

## [0.3.0](https://github.com/lezli01/vincent/compare/v0.2.0...v0.3.0) (2026-08-19)


### Features

* add a project workflow for delivering GitHub enhancement issues ([5cba419](https://github.com/lezli01/vincent/commit/5cba4195772fc4d114187d43fc7ff41acf6fb912))
* add a project workflow for filing GitHub issues ([e13e796](https://github.com/lezli01/vincent/commit/e13e79678fcfeda7c65d1f4bb96e536ca66a6a89))
* add project workflows for GitHub issues ([ffb974e](https://github.com/lezli01/vincent/commit/ffb974e6d12fb412fb89b8943cc7269a83a33820))
* agent adapter, minimal task run, M1 gate (T1.7-T1.9) ([a86553a](https://github.com/lezli01/vincent/commit/a86553a7120fd75a4bff9294b46f3deddb239b93))
* **agent,api,workflow:** T2.11 option catalog, binary-identity cache, §8.6 resolution ([5436f3c](https://github.com/lezli01/vincent/commit/5436f3cfce300812f02fc1d5f809f529ecc77e2c))
* **agent:** add the Cursor CLI adapter (T5.1-T5.3) ([8b79549](https://github.com/lezli01/vincent/commit/8b795496726d9d92a1e2dfa6ae64c84cc129ce45))
* **agent:** classify usage limits and auth failures on RunResult ([9e4b00f](https://github.com/lezli01/vincent/commit/9e4b00fb6d5f8037442a536db30d2fb09429de34))
* **agent:** export a per-adapter transcript line parser (T3.3) ([7875ec9](https://github.com/lezli01/vincent/commit/7875ec9f9d39e3c26faa1f00fc6b544ef9f8c2c5))
* **agent:** T2.9 codex adapter — exec --json runs behind the shared interface ([44980a0](https://github.com/lezli01/vincent/commit/44980a054255dd6780cd5806ab375a0c61aa8e9a))
* **api,taskrun:** POST /v1/tasks/{id}/answer + §7.4 engine test matrix ([384df72](https://github.com/lezli01/vincent/commit/384df72588357117d170f09d5c8cbd710acb1b53))
* **api,worktree:** projects API and worktree manager (T1.5, T1.6) ([ced0720](https://github.com/lezli01/vincent/commit/ced0720c64d39f3d7e238a079caad8b68ff7f8cb))
* **api,worktree:** projects API and worktree manager (T1.5, T1.6) ([d7506c0](https://github.com/lezli01/vincent/commit/d7506c0962362ee42c1c6d977b9794dfc0b631ad))
* **api:** board columns and ?archived= on the task list (T3.2) ([c57ce25](https://github.com/lezli01/vincent/commit/c57ce2584d43eb1d45b4b3dbf5a07b836f7b1da3))
* **apiclient:** action write path, input request types, diff (T3.4) ([6daef3e](https://github.com/lezli01/vincent/commit/6daef3e756f57e9bb686af72286c30d23813d51d))
* **apiclient:** daemon identity and config in effect ([a1e0406](https://github.com/lezli01/vincent/commit/a1e0406dcf33ec5c871a944a9be0325d41a15d60))
* **apiclient:** per-task stream, task detail, normalized transcripts (T3.3) ([d9bf05d](https://github.com/lezli01/vincent/commit/d9bf05d45bed6e0cb3bb5eb31dcdb7ce7db64de9))
* **apiclient:** project write path with a tri-state PATCH ([79d6c88](https://github.com/lezli01/vincent/commit/79d6c88e59653bce50638282922ea6178772a357))
* **apiclient:** projects, workflows, agents, create task (T3.5) ([c65ffca](https://github.com/lezli01/vincent/commit/c65ffca69a029212515e4040f1f0abda5f0806ff))
* **apiclient:** typed task list and info (T3.2) ([200fb08](https://github.com/lezli01/vincent/commit/200fb085356b7efda598ea020c18665a82656f32))
* **api:** expand workflow includes at task creation ([0e32bac](https://github.com/lezli01/vincent/commit/0e32bace1aa63c447c4ca5a984c272731e9a4dde))
* **api:** normalized + ranged transcripts, timeline DTO fields (T3.3) ([3280171](https://github.com/lezli01/vincent/commit/3280171e19ad8449f57dfbf2930d2bb852f7b8c4))
* **api:** POST /v1/resolve reports the §8.6 resolution (T4.7) ([1ce4296](https://github.com/lezli01/vincent/commit/1ce429646a3b26d29a085dcafd7bc01382dad653))
* **api:** POST /v1/resolve reports the §8.6 resolution (T4.7) ([795dfb0](https://github.com/lezli01/vincent/commit/795dfb0031f9280a20cc00ef1789c2d191bc37f1))
* **api:** serve one workflow's full definition (closes 017.1) ([6e20fe8](https://github.com/lezli01/vincent/commit/6e20fe8dd5d57754265f0e530298510b350a45e2))
* **api:** surface why a queued task is waiting ([ea17256](https://github.com/lezli01/vincent/commit/ea1725665387b53eca52beb07731d015cd3a3fb6))
* **api:** workflow_steps on task detail, snapshot cache invalidation (T3.4) ([0b24098](https://github.com/lezli01/vincent/commit/0b240981640d304f7f0eb1e9cf6562bd9029c4f4))
* **archive:** delete a task's branch when it has no commits past its base ([472564a](https://github.com/lezli01/vincent/commit/472564ad0961e04cca769383e4641484aaa80eca))
* **archive:** delete a task's branch when it has no commits past its base ([30cb6b5](https://github.com/lezli01/vincent/commit/30cb6b59e1e64825ec23d75474cc39734c654b19))
* classify agent usage limits and auth expiry instead of collapsing them into nonzero_exit/agent_error ([9e5e438](https://github.com/lezli01/vincent/commit/9e5e438a1731a3ff8e482d4c984455f429e3ed79))
* **cli:** project, task and workflow subcommands (T4.2, completes T5.6) ([e3fab64](https://github.com/lezli01/vincent/commit/e3fab64ddd2ac78ab4b41a4e182ea2954ce0dfbf))
* **cli:** project, task and workflow subcommands (T4.2) ([b6047df](https://github.com/lezli01/vincent/commit/b6047dff4cf95086fec0cae502c784f6da513afc))
* **codex:** normalize reasoning items to agent.thinking (T4.17) ([11370f4](https://github.com/lezli01/vincent/commit/11370f495acac471a5eee4f820402ad57fe49d69))
* **codex:** normalize reasoning items to agent.thinking (T4.17) ([8031ea3](https://github.com/lezli01/vincent/commit/8031ea3c77f27717399dca996b5007473e74d47a))
* **codex:** report logged_in from `codex login status` ([a327ccc](https://github.com/lezli01/vincent/commit/a327ccc98e87f002e0967d14f69180754538ce13))
* config & platform dirs + SQLite store & migrations (T1.1, T1.2) ([653834c](https://github.com/lezli01/vincent/commit/653834c1cdf642732fd53ecac9328642e28f8a04))
* **config,store:** platform dirs, config load/watch, SQLite store + migrations (T1.1, T1.2) ([89b06a2](https://github.com/lezli01/vincent/commit/89b06a256152ce84a0135e4c24936d38a1f74ad6))
* **config:** decide what child processes inherit (T4.23) ([77c6cd5](https://github.com/lezli01/vincent/commit/77c6cd5ef920ae7a6c0d72f0f844edf31d9993f4))
* **config:** decide what child processes inherit (T4.23) ([e39f198](https://github.com/lezli01/vincent/commit/e39f19863e340d0b5d0283e3716ff66363b8272f))
* configurable branch names (001.3-001.11) ([aaef1cf](https://github.com/lezli01/vincent/commit/aaef1cf119cd821f82846f86e34ca0695e7e938b))
* Cursor CLI adapter (T5.1-T5.6, milestone M5) ([5b407b4](https://github.com/lezli01/vincent/commit/5b407b49bbe1c3713c4bf43463890a5db8d69936))
* **daemon,api:** daemon lifecycle and HTTP API foundation (T1.3, T1.4) ([c624b1a](https://github.com/lezli01/vincent/commit/c624b1a07b84607fad4d7633f1de0e4dd3907ac6))
* **daemon:** bounded seek-from-end log tail ([1bd1c15](https://github.com/lezli01/vincent/commit/1bd1c155d309b3d3d4812ce9b87639a00e4dbfbd))
* debug record per run and a workflow column on the board (T4.12, T4.13) ([ebf3a0b](https://github.com/lezli01/vincent/commit/ebf3a0bb546e6dcf4d263f72f38957c986345725))
* debug record per run and a workflow column on the board (T4.12, T4.13) ([772bcdb](https://github.com/lezli01/vincent/commit/772bcdb8496130ab9b487a32e0f1df24d4660840))
* distribute vincent through a Homebrew tap on macOS ([832bf22](https://github.com/lezli01/vincent/commit/832bf228b5d584e06a6a961470fa75c70547c05a))
* distribute vincent through a Homebrew tap on macOS ([1e05c46](https://github.com/lezli01/vincent/commit/1e05c464365a778588c1749ba238a7238372a82d))
* **doctor:** `vincent doctor` — one command that answers "why is nothing running?" ([d8c7873](https://github.com/lezli01/vincent/commit/d8c7873b1d203f0d29080a9b5b6d20f3722017bf))
* **doctor:** one command that answers "why is nothing running?" ([e42e06f](https://github.com/lezli01/vincent/commit/e42e06f82a4c5847c84086565bbc07ff640b6fc9))
* **engine:** task FSM + step executors (T2.3, T2.4) ([5b6a27f](https://github.com/lezli01/vincent/commit/5b6a27fbb92865c399bfa89cf5df79a98104e5a7))
* **engine:** task FSM + step executors (T2.3, T2.4) ([62f0b54](https://github.com/lezli01/vincent/commit/62f0b5428942c81f8ce7f0387dc7ec910f075004))
* **events,api,taskrun,procx:** T2.7 events & SSE, T2.8 crash recovery ([d3c8cab](https://github.com/lezli01/vincent/commit/d3c8caba4acd106817e6ca1c7e003df0abc65e44))
* gate workflows that require mid-run interaction (`on_input: require`) ([1df4a01](https://github.com/lezli01/vincent/commit/1df4a0115ee7a8f21aa1fcd42a9669361a094075))
* **gate:** M1 acceptance gate script + CI job (T1.9) ([3f1d933](https://github.com/lezli01/vincent/commit/3f1d933b9ff082b57ebae04f85dd46c27b743626))
* **gate:** m2-gate.sh — workflow+input round-trip, kill -9 recovery, cap stress (T2.10) ([958c9c3](https://github.com/lezli01/vincent/commit/958c9c30322bb3d1b24fc15e8854bded7c67f023))
* **gate:** T2.10 — M2 phase gate (PR G) ([e4d0817](https://github.com/lezli01/vincent/commit/e4d08172ce403d5d53c1b676dfba98b060e48f1a))
* install the daemon as an OS-managed service (T4.1) ([9fbf70f](https://github.com/lezli01/vincent/commit/9fbf70f440ad6cc5ee901f808cba12c2146210ab))
* install the daemon as an OS-managed service (T4.1) ([2fdf8a4](https://github.com/lezli01/vincent/commit/2fdf8a465bc2c82e7e5ff3ad81e1e9d2ee8ce10b))
* let the enhancement task title carry a description ([2b21876](https://github.com/lezli01/vincent/commit/2b218761d4a8cb308b1f3066bc97c8b51a989219))
* M5 gate, restricted_unsupported reason, and a cursor example (T5.6-T5.9) ([c05751d](https://github.com/lezli01/vincent/commit/c05751da00ce47133e00e02cb0af7e4b60addce8))
* M5 gate, restricted_unsupported reason, cursor example (T5.6-T5.9) ([6a7efec](https://github.com/lezli01/vincent/commit/6a7efec11eccb29c70208b820ae2deb2c59d226e))
* parallel steps and workflow fan-out (task 014) ([67f39ef](https://github.com/lezli01/vincent/commit/67f39efbd55c8ce54230d98db46f816cd97da144))
* PR E — codex adapter + agent option catalog & selection resolution (T2.9 + T2.11) ([a10f4f0](https://github.com/lezli01/vincent/commit/a10f4f08103a4ad459e3243e5c40466453d2fb5a))
* reclaim orphaned worktrees with `vincent gc` ([7132503](https://github.com/lezli01/vincent/commit/7132503df302665ba6b06cb0fdb7d65ec9faf172))
* reclaim orphaned worktrees with `vincent gc` ([8066838](https://github.com/lezli01/vincent/commit/8066838eb9682ea8f0e79be8a393a8d7113de094)), closes [#95](https://github.com/lezli01/vincent/issues/95)
* **release:** goreleaser packaging with cosign and per-OS smoke tests (T4.5) ([26b1631](https://github.com/lezli01/vincent/commit/26b16310f54335c835ff9a27308cc5c4b592906d))
* **release:** goreleaser packaging with cosign and per-OS smoke tests (T4.5) ([b6e9054](https://github.com/lezli01/vincent/commit/b6e90544ebbba8e3183c345b429fbacccdc26fe2))
* render a tool call's subject, not its bare name (T4.14) ([24211df](https://github.com/lezli01/vincent/commit/24211df4c4f161ce728525aadddad19c3b69cfb9))
* scaffold Go module, CLI stubs, dev tooling, and CI (T0.1-T0.3) ([2c0b146](https://github.com/lezli01/vincent/commit/2c0b146d6891076a231920df437eb64df39d99f3))
* scheduler + human actions (T2.5, T2.6) ([feec51a](https://github.com/lezli01/vincent/commit/feec51a0c113d98839fc524b5e4c624fdbc1c7ac))
* **scheduler:** admission caps + human actions (T2.5, T2.6) ([52bc855](https://github.com/lezli01/vincent/commit/52bc8559f1a01500072325168286b2c82dbcb93b))
* show reasoning, tool outcomes and unwrapped lines (T4.16) ([942a2a3](https://github.com/lezli01/vincent/commit/942a2a300934c0b03766f093ef86ceaf55b070be))
* **store:** add a per-task admission hold ([24f6d2a](https://github.com/lezli01/vincent/commit/24f6d2a582e521649ed3cfae51c9c7b5ada56910))
* **store:** add the fan-out parent link and its subtree queries ([b7fd29f](https://github.com/lezli01/vincent/commit/b7fd29f1bb3cdf7ddf35a76e38867193f11b2371))
* **store:** task.step_advanced, rollups, archived filter (T3.2) ([27c4b43](https://github.com/lezli01/vincent/commit/27c4b43fed006245d15c69f9728efea44bc2bf87))
* **taskrun,store,config:** awaiting_input engine machinery (§7.4 engine half) ([6cc3aff](https://github.com/lezli01/vincent/commit/6cc3aff46a225ea7480237d5e9dd9480ad1e2743))
* **taskrun:** execute `type: parallel` step groups concurrently ([3216519](https://github.com/lezli01/vincent/commit/3216519e4828ec75bccf3a35bdf848e5d68dcd52))
* **taskrun:** minimal task run, tasks API, agent-selection columns (T1.8) ([e98b478](https://github.com/lezli01/vincent/commit/e98b478b4e8fa5bb4df8ec378153d131b7258bae))
* **taskrun:** re-queue a quota-stopped step instead of failing it ([53939d9](https://github.com/lezli01/vincent/commit/53939d9b762b7bb8de059109d4f2803a3672c4d2))
* **taskrun:** spawn, join, and manage fan-out lanes ([e92eba7](https://github.com/lezli01/vincent/commit/e92eba73f08ab6c80540d99adca3da6eaa5dc46c))
* **taskrun:** stamp live-output chunks with run_id and offset (T3.3) ([1882734](https://github.com/lezli01/vincent/commit/1882734fa7e08417fa010435facf14c4394cf98f))
* **taskstate:** add `awaiting_children` and the Settled predicate ([ee7e93c](https://github.com/lezli01/vincent/commit/ee7e93c5165bed0ca37de245b75af7ac6daaad8d))
* the output view shows what the agent is doing (T4.14, T4.16) ([f013394](https://github.com/lezli01/vincent/commit/f01339400391c48848f6336ca48f8b29526953d7))
* transcript retention pruning and a per-run size cap (T4.3) ([192843a](https://github.com/lezli01/vincent/commit/192843a25272d3f561a4530cdf36d91fa6f8e7db))
* transcript retention pruning and a per-run size cap (T4.3) ([dbdaaae](https://github.com/lezli01/vincent/commit/dbdaaae892c689a7c6c5d89cecd0d657718c5470))
* **tui:** a control-flow graph for workflows ([#131](https://github.com/lezli01/vincent/issues/131)) ([e4bd0d4](https://github.com/lezli01/vincent/commit/e4bd0d4df6da66c8d7fa719e8cb14c5004514156))
* **tui:** action bar, diff tab, answer form, edit+retry (T3.4) ([949439f](https://github.com/lezli01/vincent/commit/949439f0a130d013c0f43b6ce1c198cefad3b26c))
* **tui:** board view — live table, sort, filter, bell (T3.2) ([a49fb91](https://github.com/lezli01/vincent/commit/a49fb91eac1d63f961bef3065f85870ba7ef50e2))
* **tui:** board view (T3.2, PR I) ([33bb37e](https://github.com/lezli01/vincent/commit/33bb37ead9c1c8de83b33696abff48d5e7505f1a))
* **tui:** Bubble Tea v2 shell, shared apiclient, daemon auto-start, live SSE (T3.1) ([1937e4b](https://github.com/lezli01/vincent/commit/1937e4b3a949b9bb17f565b7974d216c7056551a))
* **tui:** command palette from a single binding registry; 1..6 retired (T3.11) ([234c647](https://github.com/lezli01/vincent/commit/234c64734e2f784d1658098dcaf7afb5b0e60b9f))
* **tui:** command palette from a single binding registry; 1..6 retired (T3.11) ([b4212af](https://github.com/lezli01/vincent/commit/b4212af4e3037e9668a34a816c9a4a19bafc5f49))
* **tui:** configurable task-table grouping, project then workflow by default ([0290761](https://github.com/lezli01/vincent/commit/02907610c556284a971a4105d10ac9642f11eb9b))
* **tui:** configurable task-table grouping, project then workflow by default ([a013038](https://github.com/lezli01/vincent/commit/a013038a72214ad194498561978f353d275dbce1))
* **tui:** contextual footer from the registry; action bars merge into it (T3.12) ([3ee3b09](https://github.com/lezli01/vincent/commit/3ee3b09867cd4d824f8843af702a5a74b3bcb53f))
* **tui:** contextual footer from the registry; action bars merge into it (T3.12) ([1097d0b](https://github.com/lezli01/vincent/commit/1097d0b0752926a44324f98588f1fe053a9d52dc))
* **tui:** daemon view & first-run full-auto notice (T3.7) ([8f8e4e2](https://github.com/lezli01/vincent/commit/8f8e4e26b611e35aa6590103173530e13246c0a2))
* **tui:** daemon view and the first-run full-auto notice (T3.7) ([7644a23](https://github.com/lezli01/vincent/commit/7644a2395b76eb1296b73bb024f71ff43495d4b5))
* **tui:** diagram, layout and renderer for workflow graphs (closes 017.2, 017.3, 017.4) ([4469930](https://github.com/lezli01/vincent/commit/4469930a9b85977e5e53cf9d3239d83451e0a80f))
* **tui:** draw an include as a collapsed workflow reference ([5f19d4f](https://github.com/lezli01/vincent/commit/5f19d4fd81b32bc4e95b2392ad700d679db98a8f))
* **tui:** group the diff tab by file, folded shut ([8b1c7d6](https://github.com/lezli01/vincent/commit/8b1c7d6d3a1c8e1f2751355836920be17a75a952))
* **tui:** group the diff tab by file, folded shut ([6fc65ca](https://github.com/lezli01/vincent/commit/6fc65caef4bb44b30ca1f929834f0cf8adcd505d))
* **tui:** mouse — click to focus and select, wheel, footer hints, tabs (T3.13) ([db53d97](https://github.com/lezli01/vincent/commit/db53d9713cc9f52ffa9409e89c3be7af9e4f8799))
* **tui:** mouse support — click to focus and select, wheel, footer hints, tabs (T3.13) ([a26ddab](https://github.com/lezli01/vincent/commit/a26ddabbab5a06534e0edd2893803da3ce546c06))
* **tui:** new-task flow — pickers, fields, overrides, create (T3.5) ([45ac322](https://github.com/lezli01/vincent/commit/45ac322df591e1d48e1fd29f0f242cade053b96e))
* **tui:** new-task flow (T3.5) ([3776d67](https://github.com/lezli01/vincent/commit/3776d67b815f366351fec1d3640bad3cd649101b))
* **tui:** open an attempt's whole transcript with e (T4.11) ([4e3d321](https://github.com/lezli01/vincent/commit/4e3d3213e15bb9e591cb2f24949e734cc2f9c510))
* **tui:** open an attempt's whole transcript with e (T4.11) ([44cba39](https://github.com/lezli01/vincent/commit/44cba39c3aa45097f42aaa51bbe91e6ab0107b6b))
* **tui:** PR H — TUI foundation (T3.1) ([bc41e9c](https://github.com/lezli01/vincent/commit/bc41e9c70b5b52045353ed66d2bcd8b78a2305f9))
* **tui:** Projects & Workflows views (T3.6) ([65c4026](https://github.com/lezli01/vincent/commit/65c402687ce84b273c20884733bbcc6b9e9f0be7))
* **tui:** Projects view — list, add, edit, delete (T3.6) ([82c5a91](https://github.com/lezli01/vincent/commit/82c5a91e1f95de94aaec526c37ca6089a10bf4a7))
* **tui:** report logged_in and window the option pickers (T5.4, T5.5) ([0c76cee](https://github.com/lezli01/vincent/commit/0c76ceef9c21ca95395b17e529ffc6d2f7c3a0d5))
* **tui:** select several tasks for one bulk action ([998f326](https://github.com/lezli01/vincent/commit/998f32690fe4f4440c692941a9f64184d34135bd))
* **tui:** select several tasks for one bulk action ([73f3bb9](https://github.com/lezli01/vincent/commit/73f3bb959658da078a4f3ce68f2c8ad71d9203d7))
* **tui:** show fan-out parents and drill into their lanes ([3c7775b](https://github.com/lezli01/vincent/commit/3c7775b053a82da6e6d0a7f74184a6d3ed0e750e))
* **tui:** task detail — diff tab, action bar, answer form, edit+retry (T3.4) ([08a76f9](https://github.com/lezli01/vincent/commit/08a76f92f5b28e4ca14da6aad2ff946da679ea50))
* **tui:** task detail — step timeline and live output tail (T3.3) ([8987af7](https://github.com/lezli01/vincent/commit/8987af75bb7423128a7f630a5ad3a535c7c587b5))
* **tui:** task detail — timeline & tail (T3.3) ([baee318](https://github.com/lezli01/vincent/commit/baee318dd5d2f3edfbd4ca8d94334d3ce2af26d7))
* **tui:** the WorkflowGraph bubble and its workflows-screen layer (closes 017.5, 017.6, 017.7, 017.9) ([99d546c](https://github.com/lezli01/vincent/commit/99d546c87618392ff1e14077e4ae5dec51848ce1))
* **tui:** Workflows view — merged registry with live reload (T3.6) ([3345f73](https://github.com/lezli01/vincent/commit/3345f73ade031af82ef218500b0515accabf0987))
* **workflow:** conditions between steps — `if:`, `type: condition`, `allow_failure:` ([2d8c223](https://github.com/lezli01/vincent/commit/2d8c223e45aa9594dd56f569a5254e187dcaf328))
* **workflow:** conditions between steps — `if:`, `type: condition`, `allow_failure:` ([0e6c41e](https://github.com/lezli01/vincent/commit/0e6c41e51b3e6d6fe61c50e311f1521e7300ed5c))
* **workflow:** including one workflow in another (`type: include`) ([d08654a](https://github.com/lezli01/vincent/commit/d08654ab19aa8ca9ccae75b7cc6a4d9623a176ed))
* **workflow:** loops — `type: loop`, `type: break`, and `.Loop` ([b82e059](https://github.com/lezli01/vincent/commit/b82e05939529f4b4eaf790479c1ce692f28dd8e2))
* **workflow:** loops — `type: loop`, `type: break`, and `.Loop` ([29a6743](https://github.com/lezli01/vincent/commit/29a6743087392c7c765fdb42211b6c3e804cde63))
* **workflow:** registry + template engine (T2.1, T2.2) ([7d181b0](https://github.com/lezli01/vincent/commit/7d181b0a98b498fffc97ae16b494967db7654e0b))
* **workflow:** registry + template engine (T2.1, T2.2) ([b2d7be7](https://github.com/lezli01/vincent/commit/b2d7be7e7365fbc53138ca36033a292928b227bc))
* **workflow:** resolve fan-out trees at task creation ([e8a29b0](https://github.com/lezli01/vincent/commit/e8a29b0fb890dbe58360aebc09ec5471b8932afa))
* **workflow:** restrict a workflow to platforms with `platforms:` ([9536828](https://github.com/lezli01/vincent/commit/9536828c16ac2d99ef6b82abc405311a1c784736))
* **workflow:** restrict a workflow to platforms with `platforms:` ([9e0b8ea](https://github.com/lezli01/vincent/commit/9e0b8ea7bf83978863e73717d549fa7e1f6d7794))
* **workflows:** add github-bug and release workflows ([3d3019d](https://github.com/lezli01/vincent/commit/3d3019d72b9c6e3c57227897d37589533f4772db))
* **workflows:** add github-bug and release workflows ([549093e](https://github.com/lezli01/vincent/commit/549093e4b1be6bd0f871f6624335f2ed58e1a3b2))
* **workflows:** add prepare-release workflow ([467dcbc](https://github.com/lezli01/vincent/commit/467dcbc872c42a4ae166e411abbe08e7318a99e4))
* **workflow:** splice an included workflow's steps at task creation ([f9fcb7b](https://github.com/lezli01/vincent/commit/f9fcb7b9bfc14800e962f8dd65e41f155d5e1008))
* **workflow:** validate `type: fan_out` and its lanes ([418cb44](https://github.com/lezli01/vincent/commit/418cb449f079178a573b43cc63b1608837b034c9))
* **workflow:** validate `type: parallel` step groups ([5074c08](https://github.com/lezli01/vincent/commit/5074c0811febc4979bf319915ee9b684a55911d5))
* **worktree:** add the branch template context and name validation ([4fb5749](https://github.com/lezli01/vincent/commit/4fb5749b0f01f687f95d88607c2616b4f865fe0b))
* **worktree:** branch template context and name validation (001.1, 001.2) ([cdcb97a](https://github.com/lezli01/vincent/commit/cdcb97ad471fe1cf442ac797c22eeb9baf765d63))


### Bug Fixes

* bake the install-time PATH into the service unit (T4.15, T4.1 macOS leg) ([aa54669](https://github.com/lezli01/vincent/commit/aa54669f574f43c96eba68407242f7dd8c1a8506))
* bake the install-time PATH into the service unit (T4.15, T4.6) ([8c97746](https://github.com/lezli01/vincent/commit/8c977466e6a3db5184eef9f560321b77fa9075fd))
* **ci:** mark m5-gate.sh executable ([8b3f01a](https://github.com/lezli01/vincent/commit/8b3f01a03a75b0c846600e3038130c1ec88120e1))
* **daemon:** join catalog probe on shutdown ([e3dc971](https://github.com/lezli01/vincent/commit/e3dc971b3cdd2fb0bb95ea8c9ef2adeee5cb8798))
* **gate:** m6's flaky sub-step chained `exit`, which pwsh rejects ([93636ef](https://github.com/lezli01/vincent/commit/93636ef58e8a04eba828f0269d00aae728f59b2f))
* **gate:** make m6's step bodies portable to pwsh ([70d9510](https://github.com/lezli01/vincent/commit/70d9510e160553a3004a155958ca8bc40d03456b))
* **gate:** make m6's step bodies portable to pwsh ([2b2f5d3](https://github.com/lezli01/vincent/commit/2b2f5d33c4da3a6bbdeb2b0b0acbb6c61c4a7e35))
* **gate:** mark m2-gate.sh executable ([714e0ce](https://github.com/lezli01/vincent/commit/714e0ce8df0403364529f859c20fce973067cb7f))
* **gate:** strip jq's CRLF from m8's multi-line captures ([8c5c444](https://github.com/lezli01/vincent/commit/8c5c4441bfe7d5df22d5ad3fec8791abcfe7984e))
* hide the console window the Windows task runs in (T4.20) ([2d6c295](https://github.com/lezli01/vincent/commit/2d6c2950013c6bc228a4c370f4284951c1e33651))
* phase 2 review findings — cancel path, retry budget, registry isolation ([aa285a6](https://github.com/lezli01/vincent/commit/aa285a68c2c7acffc714e729e20ad5193ea2815b))
* **procx,gitx:** stop daemon children flashing console windows on Windows ([130bf6a](https://github.com/lezli01/vincent/commit/130bf6ab6927bd600962d6652f44d2896171464f))
* **procx,gitx:** stop daemon children flashing console windows on Windows ([725de30](https://github.com/lezli01/vincent/commit/725de30ee8cedb18299d36974bd4692e5a656151))
* **procx:** tolerate terminated Windows process ([4449ce4](https://github.com/lezli01/vincent/commit/4449ce4a9c7bedb74414ff1c3b3c1cc5e7c06627))
* **procx:** treat darwin's EIO from kern.proc.pid as a gone process ([7624ca9](https://github.com/lezli01/vincent/commit/7624ca9e25f44b38b681286166cb1f5d2c8b63af))
* read the gate's launch environment from printenv, not $SHELL (T5.7) ([1dfd8e1](https://github.com/lezli01/vincent/commit/1dfd8e1040e588bbeefbb6d1a76f938f6c210ab2))
* release the console at logon, expire failed probes (T4.21, T4.22) ([24b48f2](https://github.com/lezli01/vincent/commit/24b48f272eeb547af5c38185640c2a89d0c190c5))
* run the Windows daemon as the invoking user, not LocalSystem (T4.19) ([9d18abd](https://github.com/lezli01/vincent/commit/9d18abde69f0ef9e2ed8a772d5bd29246fc00672))
* run the Windows daemon as the invoking user, with no window and no stale probe (T4.19–T4.22) ([b31f477](https://github.com/lezli01/vincent/commit/b31f4773604e32c41702f060f2225ae75477cf4b))
* **service:** split the exec helper by what each platform invokes ([6b0ee66](https://github.com/lezli01/vincent/commit/6b0ee6604781d7fca08944f49630c2aebdc6641a))
* **store,taskrun:** stop published events aliasing the caller's task ([aa40999](https://github.com/lezli01/vincent/commit/aa409996a243f9c70466e5019395555cb82d4064))
* **taskrun,store,scheduler:** phase 2 review fixes for cancel, retry, and admission ([14b4a1d](https://github.com/lezli01/vincent/commit/14b4a1d55ac45bcc2907b85f861a9da5d24a9a1a))
* **taskrun:** an empty for_each loop records a row (closes 018.8, 018.9) ([37a8a92](https://github.com/lezli01/vincent/commit/37a8a928ce13be76e1e402d2f1d23deb81145166))
* **taskrun:** take process teardown off the cancel request path ([216971d](https://github.com/lezli01/vincent/commit/216971d0a92d062ca742a68f81c70623d03e316a))
* **taskrun:** three control-flow correctness fixes (closes 018.1-018.7) ([978e3e5](https://github.com/lezli01/vincent/commit/978e3e51dec059dd9d551b467c45358d9af9e0e0))
* **taskrun:** three control-flow correctness fixes (closes 018.1-018.7) ([977a313](https://github.com/lezli01/vincent/commit/977a31357df768cfc2dd5455f7be2c6c68cb4e3d))
* **tui:** broadcast background messages so a takeover cannot swallow them ([118a9df](https://github.com/lezli01/vincent/commit/118a9df586647bf6c5a0a6af73e6bba64702aac7))
* **tui:** broadcast background messages so a takeover cannot swallow them ([de262cd](https://github.com/lezli01/vincent/commit/de262cd9d2ed6ccd177dd5a165fd4b5a2f4d8e2a))
* **tui:** drop stale task fetches that land after newer ones ([4cbfba7](https://github.com/lezli01/vincent/commit/4cbfba78f7503041bc6344b29bf67ca3bf0031cf))
* **tui:** frame the takeover screens; one key row, the footer's (T3.8 findings) ([a52382a](https://github.com/lezli01/vincent/commit/a52382ae231a57dfa09447dec69822de604a3c50))
* **tui:** frame the takeover screens; one key row, the footer's (T3.8 findings) ([51053f7](https://github.com/lezli01/vincent/commit/51053f7fb9f98703ae10289a3dfb54ca7a8534c6))
* **tui:** lead the adapter row with the blocking warning ([2106f08](https://github.com/lezli01/vincent/commit/2106f088cfbf93e63e2bcee9c449fbb84fab973a))
* **tui:** make [ and ] switch the output tab, and test the registry (T4.18) ([521bb53](https://github.com/lezli01/vincent/commit/521bb5320d25fb7b0391afa9ce88a7ea5af69edc))
* **tui:** make [ and ] switch the output tab, and test the registry (T4.18) ([867c117](https://github.com/lezli01/vincent/commit/867c117350791144b7223649018d95ca4e431f9f))
* **tui:** mouse row hit-testing and clipboard paste (T3.8) ([12b8975](https://github.com/lezli01/vincent/commit/12b8975af867d50009eb69f9647b75a87de31353))
* **tui:** mouse row hit-testing and clipboard paste (T3.8) ([5aca326](https://github.com/lezli01/vincent/commit/5aca3269ee16fc6791dea23caa071ee8da2d7d20))
* **tui:** reopen the tracked row when a settle window fired into a takeover ([418adcd](https://github.com/lezli01/vincent/commit/418adcdca56475435828af0ce4bd3a779614b917))
* **tui:** walkthrough polish — clicks, project columns, contextual help, palette sections (T3.8) ([9c5a3cc](https://github.com/lezli01/vincent/commit/9c5a3cc89371b071cd4bcdfe57a1c48f90fef88a))
* **tui:** walkthrough polish — clicks, project columns, contextual help, palette sections (T3.8) ([b2da344](https://github.com/lezli01/vincent/commit/b2da34448fbafe3ac64ea615d145ffed245d13a1))
* **tui:** widen the answer popup and wrap long free-text answers ([fb2b3bb](https://github.com/lezli01/vincent/commit/fb2b3bb850758c37ef863fe8cc2d1103cf17a935))
* **tui:** widen the answer popup and wrap long free-text answers ([efb26c1](https://github.com/lezli01/vincent/commit/efb26c1614c5b153fe7a6b13b1e74272c7382205))
* **tui:** wrap the answer form instead of truncating it ([df88ea4](https://github.com/lezli01/vincent/commit/df88ea4e7553989d186feb7d0580557e3a8ae0cd))
* **tui:** wrap the answer form instead of truncating it ([e03ad9b](https://github.com/lezli01/vincent/commit/e03ad9b9bede221a359b374cf4976110c36384b2)), closes [#83](https://github.com/lezli01/vincent/issues/83)
* **version:** report the module version for go install builds ([d6ffa94](https://github.com/lezli01/vincent/commit/d6ffa94390f75ca102b40cbbd6cb27540a9f9223))
* Windows testing findings — concurrent control requests and three docs defects (T4.8-T4.10) ([54ec08c](https://github.com/lezli01/vincent/commit/54ec08c226a4cd36f049ed3e2e04941fc90fd6e6))
* Windows testing findings — concurrent control requests and three docs defects (T4.8-T4.10) ([7be6efa](https://github.com/lezli01/vincent/commit/7be6efa4a951fff97f8a99dba07e856ef6f4acd8))
* **workflow:** duplicate-name isolation, failure-block newline, watcher edges ([2994d2a](https://github.com/lezli01/vincent/commit/2994d2a2d7c2a91980abea3d0e6532a342a033b5))

## [0.2.0](https://github.com/lezli01/vincent/compare/v0.1.1...v0.2.0) (2026-08-16)


### Features

* **codex:** report logged_in from `codex login status` ([7c1a506](https://github.com/lezli01/vincent/commit/7c1a506ef826d2508c1164eb484d2d07067e9783))
* **doctor:** `vincent doctor` — one command that answers "why is nothing running?" ([e298676](https://github.com/lezli01/vincent/commit/e298676533dfab7cab42693dc4ff2be36590cb59))
* **doctor:** one command that answers "why is nothing running?" ([e0d63c7](https://github.com/lezli01/vincent/commit/e0d63c7f99b8ad04e72c72cb6e64cff49cfac0b5))
* reclaim orphaned worktrees with `vincent gc` ([e1195e4](https://github.com/lezli01/vincent/commit/e1195e4f6e611e68a6c25d20dae6beb6f940bfbf))
* reclaim orphaned worktrees with `vincent gc` ([5a9037d](https://github.com/lezli01/vincent/commit/5a9037d8392fb32b9e539f072d5fd75c73429743)), closes [#95](https://github.com/lezli01/vincent/issues/95)

## [Unreleased]

### Added

- **A control-flow graph for workflows in the TUI — `g`.** The workflows screen
  explained a workflow as a numbered list of its top-level steps, which was
  enough while workflows were linear. The language now has structure —
  `parallel` groups, `fan_out` lanes and their merge, guards, `condition`,
  `loop` and `break` — and a list can name those constructs without showing
  where control goes. `g` on an entry draws it.

  The graph opens *over* the registry list rather than replacing it: `enter`'s
  step list still carries the findings, platform notes and agent resolution the
  picture does not show, and `esc` closes one layer at a time. Arrows move the
  selection and the view follows it; `shift`+arrows pan; a graph larger than the
  terminal is cropped and panned, never reflowed into a different shape. `e`
  works from inside the layer, so saving the file in your editor redraws the
  graph in place with the same node still selected.

  Everything the picture says survives having colour stripped: frame weights
  separate a `parallel` group from a `fan_out` from a `loop`, boxes carry the
  step's own type word, and a `condition`'s two ways out are labelled `true` and
  `false`. A `fan_out` shows a merge node because its join is a git merge that
  runs and can block; a `parallel` group shows none, because its join is only
  its members finishing. A guard on an ordinary step draws no second branch —
  false there means skip and carry on.

  A new endpoint backs it: `GET /v1/workflows/definition?name=&project_id=`
  serves one workflow's whole recursive structure, as authored, with workflow
  defaults kept in their own block. The registry list keeps its compact shape.

- **Loops in workflows — `type: loop` and `type: break`.** A workflow can now
  repeat a body of steps: `count:` a fixed number of times, or `for_each:` once
  per item in a list, including a list a step discovered at run time. That
  makes three shapes writable that were not: **converge** ("run the tests, fix
  what broke, run them again" — a probe under `allow_failure:`, a `break`
  reading it, a repair), **repeat** (ten passes of the race detector without
  ten copy-pasted steps), and **iterate a set** (once per changed file). A loop
  is one step, one index, one concurrency slot and the task's one worktree — no
  branch and nothing to merge, which is what separates it from `fan_out`.

  `type: break` ends the loop successfully when its `if:` is true. There is no
  `continue` type: a `condition` inside a loop body ends *that iteration*,
  which is what continue means, using the meaning that word already had. There
  is no `while:` either — a guard can only read what a run has produced, so a
  `while:` about its own body is either loud on iteration 1 or silently false,
  and `count:` plus `break` is the same loop written correctly.

  `.Loop` (`Index`, `Item`, `IsFirst`, `IsLast`) joins the template context,
  with `Index: 0` outside any loop. `loop.max_iterations` (default **10**) is
  the ceiling: `count:` is checked against it when the file loads, and a
  `for_each` list longer than it blocks with `loop_limit` before the first
  iteration rather than quietly doing the first ten — ten iterations of a
  three-step body is already thirty agent runs. A loop's position is derived
  from its step rows on every admission and never persisted, so `retry` and a
  daemon restart both resume **mid-iteration**; `skip` skips the whole loop,
  and `edit + retry` on a body step applies to every remaining iteration. The
  board shows `loop 4/10` and the detail view groups rows by iteration, folded
  with the latest open. See
  [`type: loop`](docs/reference/workflow-schema.md#type-loop).
- **Conditions between steps.** A workflow can decide at run time what to do
  next. `if:` on any step is a guard: false skips that step and the workflow
  carries on, recording a `skipped` row whose reason says a condition did it
  rather than you. On a fan-out lane or a `parallel` sub-step the same `if:`
  subsets the set instead — the others still run and the join still happens.
  `type: condition` is a step whose whole body is the guard: false ends the run
  and the task is `done`, which is how a workflow finishes early. And
  `allow_failure:` on agent and command steps turns the failures a step itself
  produced into an advance, so a guard has something a run *discovered* to
  read — without it, a guard could only see what you typed when you created the
  task. Guards are ordinary templates that must render exactly `true` or
  `false`, are re-evaluated every time rather than cached, and can now read
  `.Host.OS`. See [Conditions](docs/reference/workflow-schema.md#conditions).
- **`type: parallel` — sub-steps that run at once.** A group runs its
  sub-steps concurrently in the task's one worktree: one step, one index, one
  concurrency slot, no branch and no merge. It succeeds when every sub-step
  does, a failure does not cancel its siblings, and a retry re-runs only what
  failed. `parallel.max_parallel` (default 4) bounds it, and is a **second
  concurrency dimension your task caps do not govern** — a board reading "1
  running" can be a machine running four compilers. `manual`, nested groups
  and `on_input: require` are refused inside a group.
- **`type: fan_out` — lanes as real child tasks.** Each lane becomes an
  ordinary task with its own worktree, branch, retries, gates and blocks, and
  their branches are merged back (`--no-ff`, in declared order) into the
  branch the task already owns, so one branch is still delivered. A lane is a
  named workflow or inline steps, resolved into the task's snapshot at
  creation; lanes may nest to any depth, bounded by `fan_out.max_depth` (3)
  and `fan_out.max_tasks` (64), both checked at creation with a `400` naming
  what is wrong.

  A merge conflict blocks the task with `merge_conflict` and leaves the
  worktree conflicted so you resolve it in place, stage, and retry;
  `merge: {on_conflict: agent}` opts into an agent attempt first. A lane that
  is cancelled or ends without finishing blocks with `lane_failed` and merges
  **nothing**.

  Two things worth knowing before you use it: a fan-out **fills** your
  concurrency caps rather than exceeding them, and N lanes leave N worktrees
  on disk until the tree is archived.

  New `awaiting_children` task state (holds no slot), `?parent_id=` and
  `?include_children=` on `GET /v1/tasks`, a `children` rollup on the task
  detail, the `task.children_changed` event, `vincent task ls
  --include-children/--parent`, and `L` in the TUI to drill into a fan-out's
  lanes.

### Fixed

- **A fan-out whose spawn failed part-way could never be retried.** Lanes were
  created one at a time, each committing before the next, so a failure on lane
  two left lane one committed; the cleanup cancelled it, and a cancelled lane
  stays attached to its step. The parent's `retry` therefore found a lane, took
  the *join* path instead of re-spawning, read the lane as aborted and blocked
  `lane_failed` — again on every retry, with nothing in the API or the TUI able
  to clear it. Lanes are now inserted in one transaction, so a failure leaves no
  lane behind and `retry` re-spawns from a clean slate.

- **A fan-out could join lanes that had not started.** The parent decided
  *spawn or join* on whether the step had lanes, which only answers "have the
  lanes finished" if the park after spawning always commits — and it is a
  compare-and-swap. A parent left `running` with `queued` lanes joined on its
  next admission and blocked `lane_failed` against work about to run perfectly
  well. It now parks again instead.

- **A `parallel` sub-step's guard could read a sibling after a retry.** §7.5
  says a group is a set whose members cannot see each other, and that held on a
  group's first admission only: a re-admitted group skips the sub-steps that
  already succeeded, and their rows were still visible in `.Steps`. The same
  guard against the same context answered one way on the first run and another
  after a human pressed `retry`. Set-invisibility now holds in every admission.

- **A `loop` whose `for_each` list re-derived shorter than its own rows.** The
  extent came from the fresh list alone, so a shorter one would have left the
  loop reporting success over iterations it started and never revisited. The
  extent is now the longer of the list and the recorded iterations, with the
  `max_iterations` ceiling re-checked against it. Every `for_each` source §8.4
  offers is stable between admissions, so this bounds the derivation rather than
  a reachable failure.

- **A `loop` with an empty `for_each` list left its step index with no row.**
  The two structure steps each have a case where they are reached and run
  nothing — every `fan_out` lane guarded off, an empty `for_each` list — and only
  the fan-out recorded a row saying so, leaving a detail view unable to tell
  "ran nothing" from "never reached" for the loop. The empty case now records
  one row under the loop's own id. That row is deliberately not a `.Steps`
  entry: a loop's id is never one, or it would be a key present exactly when the
  loop did nothing and absent when it did something.

- **A leaked context in `parallel` and `loop` steps.** Both created a
  cancellable context and then overwrote it — `cancel` included — when the step
  carried a `timeout:`, leaving the first context attached to the parent for the
  rest of the task's run.

### Changed

- **vincent is now source-available and dual-licensed, not MIT.** Personal and
  non-commercial use is free under the
  [PolyForm Noncommercial License 1.0.0](LICENSE); commercial or business use —
  including running vincent inside a for-profit company's own development
  workflow, without selling it — requires a separate commercial license, see
  [COMMERCIAL-LICENSE.md](COMMERCIAL-LICENSE.md). This is deliberately *not* an
  OSI-approved open-source license: restricting commercial use is incompatible
  with the Open Source Definition, so "source-available" is the accurate word.

  **The change is not retroactive.** `v0.2.0` and every release before it were
  published under the MIT License and stay usable under it forever, on the terms
  they shipped with; no tag or published artifact is modified. The first release
  published after this change is the first release under the new licensing, and
  every release after it follows.

### Added

- **`vincent gc` reclaims directories no task claims.** A worktree could outlive
  every reference to it: deleting a project removes worktrees best-effort and
  drops the task rows regardless, so a removal that failed — a file locked by
  another process, a shell sitting in the directory — left a directory nothing
  could ever name again. A crash between creating a worktree and recording its
  path did the same, and made the task's next admission fail
  `worktree_path_occupied` pointing at a directory the user had never been told
  about.

  `vincent gc` lists those directories with their sizes and removes them.
  `--dry-run` prints the identical report and removes nothing. A worktree with
  local changes (untracked files count) is skipped as `worktree_dirty`; one whose
  repository is gone, so `git status` cannot run at all, is skipped as
  `dirty_unknown` — the common case for a real orphan — and `--force` removes
  both. Orphaned transcript directories are reclaimed too, since retention walks
  task *rows* and a cascade-deleted row's transcripts are reached by no pass.
  Nothing outside `{data_dir}/worktrees` and `{data_dir}/transcripts` is ever
  removed, no branch is ever deleted, and no task row is modified.

  New endpoints `GET /v1/maintenance/orphans` and `POST /v1/maintenance/gc`, and
  `orphans` on `GET /v1/info`.

- **The daemon reports orphans at startup and never deletes them.** One warning
  per directory in the log, plus the count on `/v1/info` and a line in the TUI
  daemon view naming `vincent gc`. The unattended path reports; a human deletes.

- **`scripts/install-local.sh`** installs the checked-out tree as a regular
  local install: one binary on PATH, built with the release flags from
  `.goreleaser.yaml` and the same three version symbols, so `vincent version`
  names your commit. Defaults to `/usr/local/bin` (`--user` for `~/.local/bin`,
  `--bin-dir` for anywhere else), and reports the two things that make a fresh
  build look like it did not take: another `vincent` shadowing it on PATH, and
  a daemon still running the previous binary. `--uninstall` removes the binary
  and leaves config, database and transcripts alone.

- **`vincent doctor` — one command that answers "why is nothing running?".**
  It used to take five surfaces and a hand-extracted bearer token: `daemon
  status`, reading `daemon.json` yourself, the TUI's daemon view for a log tail,
  `curl /v1/agents`, and finding the config file. Doctor prints all of it in one
  pass — the config and data directories and whether `config.yaml` parses; the
  daemon's status, pid, port, uptime and version; the log's size and last lines;
  the database's size, schema version and `PRAGMA integrity_check`; every agent
  CLI with its path, version and `logged_in`; free disk, worktree count and
  bytes, and orphaned worktrees; and task counts by state. `--json` emits the
  whole report for scripting and for pasting into a bug report.

  It exits `0` healthy, `1` on problems found, `2` when no daemon answered — and
  **it still prints a full report with no daemon running**, since that is one of
  the answers. In that mode the database and task rows read *unknown — daemon
  not running* rather than opening SQLite behind the daemon's back. A missing or
  logged-out agent CLI is reported plainly and deliberately does not set the exit
  code.

- **`vincent doctor --fix` reclaims orphaned directories and compacts the
  database.** Doctor reports the same orphans `vincent gc` does and `--fix` runs
  the same reclaim — one definition, one removal path, so the two commands
  cannot disagree about what is safe to delete. What doctor adds is the report
  beside the disk figures, and the database, which grew forever with no surface
  naming its size. Both writes are performed by the daemon. Two refusals are by
  design and are reported rather than hidden: an orphan with local changes is
  skipped until `--force`, and the `VACUUM` is skipped while any task is in
  flight instead of stalling it mid-step. With no daemon answering, orphans read
  *unknown* — the claim set lives in a database only the daemon opens.

- **`GET /v1/doctor` and `POST /v1/doctor/fix`** serve the report and the
  repair. Agent availability on `/v1/doctor` is re-probed unconditionally,
  unlike `GET /v1/agents`: authentication is not a function of the binary, so a
  cached `logged_in: false` would survive you logging in.

### Changed

- **Releases are managed by Release Please.** Conventional Commits maintain a
  release pull request; merging it creates the version tag and GitHub release,
  then the existing GoReleaser workflow publishes signed artifacts and updates
  the stable Homebrew cask. Prerelease tags continue to skip the tap.

- **Archiving a task now deletes its branch when that branch has no commits past
  its base.** A workflow that files an issue, posts a summary or reviews
  read-only writes nothing to the repository, but it still got a branch, and
  archiving it left that branch behind forever — one empty `vincent/*` ref per
  run, with no reliable glob to clean up with, since branch names are
  configurable. The only remedy was to list the archived tasks and delete them by
  hand.

  The test is exact: `git rev-list -n 1 {base}..{branch}` must produce nothing,
  meaning the tip is an ancestor of the base the task was cut from. **A branch
  carrying any commit is never touched**, the delete is `git branch -d` rather
  than `-D`, and anything git cannot answer — the base branch renamed away, the
  repository gone — keeps the branch and is logged. A branch problem never fails
  an archive: the task still reaches `archived`, and the branch simply survives,
  which is what always used to happen.

  Set `delete_empty_branch_on_archive: false` in `config.yaml` for the previous
  behaviour on every path. `POST /v1/tasks/{id}/archive` reports what it did in a
  new `branch` object beside the task, and the TUI says it in one line;
  `DELETE /v1/projects/{id}?force` applies the same rule to every row it is about
  to drop, because the cascade erases the branch names for good. `vincent gc` and
  `vincent doctor --fix` still delete no branch at all — an orphaned directory has
  no task row, so there is no base branch to judge it against.

- **`delete_remote_branch_on_archive` deletes the upstream counterpart too — off
  by default.** It runs only when you archive a task yourself, only after the
  local branch was deleted, and only when the branch has a configured upstream;
  a project delete never touches a remote. A rejected push, an unreachable host
  or a timeout is logged and the archive still succeeds. It is off by default
  because a forge is shared with other people and the deletion cannot be undone.

- **codex now reports `logged_in`.** `Detect` probes `codex login status`, so
  "the CLI is installed but your session expired" is visible on the board, in
  the new-task form, in `GET /v1/agents` and in doctor — instead of first
  appearing as a task that burned its whole retry budget. The parse never
  guesses: a non-zero exit is `false`, an explicit negative is `false`, an
  explicit positive is `true`, and anything else — including a probe that times
  out or cannot be spawned — stays `null`. **claude** keeps `logged_in: null`,
  because its CLI exposes no non-interactive auth surface at all and the only
  definite answer would be a billed prompt round-trip.

## [0.1.1] — 2026-08-15

### Added

- **Repository workflows for GitHub issues and releases.** The checked-in
  project registry now includes `github-issue`, `github-enhancement`,
  `github-bug`, `prepare-release` and `release`. `github-issue` turns a rough
  task into a `bug` or `enhancement` issue, parks in `awaiting_input` while
  Claude asks for missing detail, and puts a manual gate before the
  non-retrying `gh issue create`; it is POSIX-only and deliberately leaves its
  empty worktree branch behind.

  `github-enhancement` takes an open enhancement's id as the first token of the
  task title — `42`, `#42` and a full issue URL still work, and optional prose
  after that token now produces a useful branch and PR name — then separates
  clarification from implementation, runs the expensive cross-platform check
  without an agent retry, and gates the diff before the non-retrying push and PR
  creation. `github-bug` likewise proves a regression test red before fixing it.
  Both are Claude-only and POSIX-only; codex and cursor do not support the
  `on_input` clarification they rely on.

  `prepare-release` audits all six build targets, dependencies, archive
  contents, smoke assertions and pinned actions, lets an agent clear FAIL
  findings, and verifies a real `dry_run` artifact without publishing anything;
  its task-title version is optional. `release` follows `RELEASING.md` through
  preflight and the changelog PR, then stops at a manual gate before the tag.
  Its PR and tag steps do not retry, and everything after the tag only verifies
  the published result. `release` is Claude-only and POSIX-only.

- **Explicit child-process environments.** The new
  [`environment`](docs/reference/configuration.md#environment) config block
  applies to agent steps, command steps and checks: `inherit` accepts `all`
  (the unchanged default), `none` or a list of names; `unset` removes names
  next; and literal `set` values win last. An empty inherit list means nothing,
  `$` is not expanded in `set`, and a step's own `env` still wins. This makes
  hermetic runs possible and, on Windows, lets `unset: [MSYSTEM]` stop Cursor
  importing Claude Code hooks through the MSYS environment. The daemon logs
  resolved variable names, never their credential-bearing values or the values
  in a transcript, and warns rather than rewriting an environment with no
  `PATH` or Windows `SystemRoot`.

- **Richer output and complete transcripts in the TUI.** Tool calls now show a
  useful subject rather than only `Bash` or another bare tool name, followed by
  their success or failure; reasoning is marked with `·`, calls with `▸`, and
  outcomes are indented beneath them. Claude, codex and cursor all surface tool
  outcomes. Claude and cursor surface complete reasoning blocks, and codex now
  does too when its CLI emits the effort-dependent `reasoning` item. Long
  assistant text wraps with a hanging indent instead of disappearing past the
  output pane's right edge; tool output bodies remain in the raw transcript.

  Pressing `e` from either detail pane opens the selected attempt's complete raw
  JSONL transcript in `$EDITOR`, including the beginning omitted by the TUI's
  256 KB tail. The truncation notice advertises the binding, and a pruned
  transcript, a gate with no transcript, or an editor failure is reported
  instead of opening a misleading empty file.

- **Agent usage limits are now a wait, not a failure**
  ([task 003](docs/tasks/003-usage-limit-classification.md)). When a step stops
  because the agent's usage quota for the window is spent, vincent records the
  attempt as `usage_limit`, **consumes no retry**, releases the task's
  concurrency slot, and re-queues it until the window reopens — the step then
  re-runs with **no human action**. Previously that run was indistinguishable
  from a genuine failure: it burned the whole retry budget in seconds (there is
  no delay between attempts) and blocked the task with `agent_error`, which sent
  you to read a transcript about a task that was fine. With several tasks running
  on the same agent, the whole board went down at once.

  A held task shows when it resumes — `queued → 14:20` on the board,
  `queued · usage limit → 14:20` in the detail header — and gives up its slot, so
  other work carries on. The resume time is the reset the CLI reported; when it
  reports none, vincent waits
  [`usage_limit_recheck_interval`](docs/reference/configuration.md#usage_limit_recheck_interval)
  (new, default 15m) and tries again. Cancelling, pausing or resuming a held task
  drops the wait immediately.

  Only the **claude** adapter recognizes usage-limit wording today. Capturing a
  real quota exhaustion means burning a real five-hour window, so codex and
  cursor deliberately ship no pattern and behave exactly as before — a wrong
  guess would park a genuinely failed task in a wait it never leaves.

- **`agent_unauthenticated` block reason**
  ([task 003](docs/tasks/003-usage-limit-classification.md)). A claude step that
  fails because the CLI is not logged in now says so, instead of surfacing as
  `nonzero_exit` or `agent_error`. Nothing else changes: the step still runs, the
  retry budget still applies, and the task still blocks — the reason just names
  the fix.

- `queued_reason` and `admit_not_before` on every task in the API and on
  `GET /v1/config`'s `usage_limit_recheck_interval`. Both task fields are `null`
  for an ordinarily queued task, so the addition is invisible to existing
  clients, and they are separate from `block_reason`, which still means only
  "stopped, needs a human".

- **Homebrew install on macOS** ([task 002](docs/tasks/002-homebrew-tap.md)).
  `brew install lezli01/tap/vincent`. The cask clears the quarantine attribute
  during install, so macOS users no longer meet the Gatekeeper "unidentified
  developer" prompt or have to run `xattr -d com.apple.quarantine` by hand — the
  release binaries are still cosign-signed rather than Apple-notarized, so the
  archive path is unchanged. `brew uninstall --zap vincent` also unloads the
  LaunchAgent and removes the config and data directory. Linux and Windows keep
  the release archives and `go install`.

- **Configurable branch names**
  ([task 001](docs/tasks/001-configurable-branch-names.md)). A task's branch no
  longer has to be `vincent/{id}-{slug}`. Names resolve through a chain, most
  specific first: a per-task literal (`vincent task add --branch feat/OPS-123`, or
  `branch_name` on `POST /v1/tasks`), a project template
  (`branch_template` on `PATCH /v1/projects/{id}`), the global
  [`branch_template`](docs/reference/configuration.md#branch_template) in
  `config.yaml`, and finally the built-in name. **The default is unchanged**, so
  nothing moves unless you configure it.

  Templates get `{{.ID}}`, `{{.Title}}`, `{{.Slug}}`, `{{.BaseBranch}}`,
  `{{.Fields.NAME}}`, `{{.Project.*}}` and a `slug` function. The new-task form
  previews the resolved name and the level it came from as you type, via
  `POST /v1/resolve`.

  Two things to know. Because vincent never deletes branches, a template with no
  discriminator in it collides on the *second* task for the same input — put
  `{{.ID}}` in it or expect to name repeats by hand. And `{{.Fields.x}}` fails
  loudly on a missing field while `{{ index .Fields "x" }}` renders nothing, which
  yields a legal-but-wrong name like `feat/-fix-login`; prefer the first.

- `branch_override` on `POST /v1/tasks/{id}/retry`, which makes a `branch_exists`
  block recoverable: the branch is renamed and the task re-admitted, keeping its
  id and history. Previously nothing in the API could change a branch name.

- `branch_name_invalid` block reason. Branch names are validated by
  `git check-ref-format` rather than a hand-rolled matcher, and a rejected name is
  reported rather than silently rewritten.

- `go run mage.go vuln` and a weekly `Vulnerabilities` workflow: govulncheck
  over the module's reachable code, swept across `linux`, `darwin` and `windows`
  because 15 packages (`x/sys/windows/svc`, `modernc.org/libc/*`) reach the
  binary on Windows only.
- `CHANGELOG.md`, [`RELEASING.md`](RELEASING.md) and `.github/CODEOWNERS`.
- `go install github.com/lezli01/vincent/cmd/vincent@latest` as a documented
  install path, and a versioning-and-stability policy in the README and
  `SECURITY.md`.

### Changed

- **Normalized transcript and live-output clients must read structured tool
  records.** On
  `GET /v1/tasks/{id}/steps/{run_id}/transcript?format=normalized` and the
  per-task SSE stream, `agent.tool_use.tools` changed from `[]string` to
  `[{name, summary, call_id}]`; `agent.tool_result` adds
  `results: [{call_id, name, summary, is_error}]`, and `agent.thinking` adds
  whole-block reasoning text. Clients that rendered each tool as a string must
  read its `name` field and should tolerate the two new record types.
  Normalization happens on read, so stored raw transcripts need no migration
  and gain the richer rendering retroactively.

- A task's branch name is now resolved and written inside the same transaction as
  the task row, so no committed task can carry an empty one. This removes a window
  in which a crash between two writes left the name unset — harmless while names
  were derived from `(id, title)`, but it would have silently discarded a
  configured name and run the task on a default branch.
- `docs/` is no longer versioned: `docs/versions/v0/spec.md` is now
  [`docs/spec.md`](docs/spec.md), the single platform spec, amended in place.
  Planned work lives in [`docs/tasks/`](docs/tasks/README.md), the closed v0 ledger
  in `docs/history/`, and the gate walkthroughs in `docs/gates/`.
- A `go install`ed binary now reports the module version from `vincent version`
  instead of `dev`.
- CI runs the three acceptance gates as steps of one per-OS job rather than
  three separate jobs, sharing a checkout and a toolchain setup.
- Release notes are grouped into Features / Bug fixes / Other changes instead of
  one flat list of commit SHAs.
- Third-party actions in the release workflow are pinned to commit SHAs; every
  job carries a `timeout-minutes`.

### Fixed

- **Release binaries no longer depend on a stale vulnerable Go patch.** The
  module and release workflow now pin Go 1.26.6, which clears five reachable
  standard-library advisories across linux, darwin and windows instead of
  accepting whichever 1.26 patch happens to be on a runner. A weekly workflow
  proposes patch-only toolchain bumps within the declared minor series, and
  admits them only after build, test and the vulnerability sweep pass.

- **Installed daemons now start as the right user with a usable `PATH`.** macOS
  launchd and Linux systemd units capture the installing shell's `PATH`, so
  Homebrew, npm, nvm and `~/.local/bin` agent CLIs are visible after login;
  reinstall the service after changing that path. Windows now uses an
  unelevated, per-user Scheduled Task at login instead of a LocalSystem service,
  so the daemon shares the user's data, token, git config and agent credentials.
  The task pins the new internal `vincent daemon --config-dir` and `--data-dir`
  flags; only removing a legacy LocalSystem service still needs Administrator.

- **Windows login no longer leaves a daemon console open or a slow agent
  permanently unavailable.** The scheduled daemon's `--hide-console` path now
  releases the Windows Terminal console safely and starts agent probes without
  replacement windows, while a manually entered flag leaves the user's terminal
  alone. Failed probes expire after one minute instead of being cached for the
  daemon's lifetime, cold-login probes get 20/25-second bounds, and a timed-out
  Cursor status probe is no longer reported as definitely unauthenticated.
  `vincent service status` points users to `Task Scheduler Library\\vincent`, not
  `services.msc`, and diagnoses elevated task ownership that would prevent a
  later unelevated reinstall or uninstall.

- **The advertised output-tab keys now work.** `[` and `]` switch between the
  detail view's output tabs from either pane, as the help, palette and footer
  already promised; the existing `d` alias continues to make the same toggle.

- **The TUI answer form no longer truncates what it asks you**
  ([#83](https://github.com/lezli01/vincent/issues/83)). A question, an option
  label or a permission summary longer than the popup's inner width was cut with
  an `…` and there was no way to see the rest from inside the form — no wrap, no
  scroll, no expand — and because the popup is capped at 76 columns, a wider
  terminal did not help. That hid the end of an agent's question, the
  `(Recommended)` suffix agents put at the *end* of an option label, and, in
  `restricted` mode, the tail of the command you were being asked to approve.
  Rows now wrap inside the popup, with continuation lines indented under the
  marker so a wrapped option still reads as one option; `up`/`down` still move a
  whole option at a time, and the focused row is kept fully on screen.

## [0.1.0] — 2026-08-12

First release. All 70 tasks of the
[v0 breakdown](docs/history/v0-tasks.md) are complete, and the M1, M2, M4 and
M5 acceptance gates are met.

### Added

- **Daemon** owning all state and execution — SQLite (WAL, single writer),
  git worktrees, and agent CLI subprocesses. Work continues with no client
  attached; crash recovery finalizes interrupted step runs and kills verified
  orphans (PID *and* start time must match).
- **Localhost REST + SSE API** with bearer auth, the single interface every
  client uses.
- **Workflow engine** — YAML registry with builtin < global < project
  shadowing, live reload, and three step types (agent, command, manual) plus
  retries, timeouts and human actions.
- **Bubble Tea TUI** with six views (board, detail, new-task, projects,
  workflows, daemon), holding no state the daemon does not have.
- **CLI subcommands** over the same API.
- **Agent adapters** for Claude Code, Codex and Cursor. Capability differences
  are documented and ignored at run time, never emulated.
- **OS service registration** on Windows (Scheduled Task, as the invoking
  user), macOS (launchd) and Linux (systemd user unit).
- **Signed releases** — cross-compiled archives for linux/darwin/windows on
  amd64/arm64, SHA-256 checksums, a keyless cosign signature over the checksum
  file, and GitHub build provenance attestations.

### Security

- Agents run **full-auto by default** — a documented design decision
  ([spec §16](docs/spec.md)), surfaced once by the TUI on first run.
  Git worktrees isolate collisions between tasks, not privileges.
- The daemon's trust boundary is the OS user: loopback-only listener, bearer
  token stored `0600` in the data directory, and no agent credentials stored.

## Versioning and stability

vincent is `0.x`. Until `1.0.0`:

- **Breaking changes may land in any minor release** (`0.1` → `0.2`), and are
  called out under a `### Changed` heading here with the migration needed.
- **Patch releases** (`0.1.0` → `0.1.1`) are fixes only, with no breaking
  change to the config file, the workflow YAML schema, the REST API or the CLI
  flags.
- The **config file, workflow schema, REST API and CLI surface are not stable
  yet.** Pin a version if you script against them.
- The **on-disk database migrates forward automatically** and is append-only by
  policy (`internal/store/migrations/`); downgrading a binary across a
  migration is not supported.

[Unreleased]: https://github.com/lezli01/vincent/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/lezli01/vincent/releases/tag/v0.1.1
[0.1.0]: https://github.com/lezli01/vincent/releases/tag/v0.1.0
