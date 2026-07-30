# Graph Report - .  (2026-07-30)

## Corpus Check
- 53 files · ~83,052 words
- Verdict: corpus is large enough that graph structure adds value.

## Summary
- 738 nodes · 1704 edges · 33 communities (31 shown, 2 thin omitted)
- Extraction: 86% EXTRACTED · 14% INFERRED · 0% AMBIGUOUS · INFERRED: 231 edges (avg confidence: 0.8)
- Token cost: 109,162 input · 0 output

## Community Hubs (Navigation)
- Diff View Rendering
- Claude Session Probing
- Release and Project Docs
- Create and Task Forms
- Settings and Config Tests
- Prompt Template Rendering
- Git Worktree Operations
- CLI Commands and Auth
- YAML Config Editor
- ClickUp API Client
- TUI App Shell
- TUI Layout Helpers
- Session State Store
- Config Schema
- Worktree Clean View
- Theme and Styles
- Demo UI Surface
- Local Review Rendering
- Claude Agent Runner
- Doctor Health Checks
- PR List View
- Direct Create and Review Entry
- Session Upsert and Branch Context
- App Tab Routing
- CLI Command Dispatch
- Branch Naming
- Local Review Flow Tests
- Worktree Cd Picker
- Diff Command and Numstat
- Demo Recorder Script
- Tasks Tab Tests
- Go Module Root

## God Nodes (most connected - your core abstractions)
1. `Config` - 46 edges
2. `DiffView` - 46 edges
3. `main()` - 25 edges
4. `settingsModel` - 23 edges
5. `Dashboard` - 21 edges
6. `App` - 20 edges
7. `createModel` - 16 edges
8. `Worktree` - 15 edges
9. `OpenEditor()` - 14 edges
10. `Store` - 14 edges

## Surprising Connections (you probably didn't know these)
- `shell-init cd integration` --semantically_similar_to--> `Homebrew cask distribution`  [INFERRED] [semantically similar]
  README.md → .goreleaser.yaml
- `cmdContext()` --calls--> `ChangedFiles()`  [INFERRED]
  cmd/norn/main.go → internal/git/git.go
- `cmdTemplates()` --calls--> `DataFields()`  [INFERRED]
  cmd/norn/main.go → internal/prompt/prompt.go
- `Direct create shortcut` --semantically_similar_to--> `New tab (create worktree from hint)`  [INFERRED] [semantically similar]
  CLAUDE.md → README.md
- `Headless cost and permission gates` --semantically_similar_to--> `Graceful degradation of optional tooling`  [INFERRED] [semantically similar]
  ROADMAP.md → README.md

## Import Cycles
- None detected.

## Hyperedges (group relationships)
- **Tag-to-Homebrew release pipeline** — _github_workflows_release_release_workflow, _goreleaser_norn_build, _goreleaser_homebrew_cask, _github_workflows_release_homebrew_tap_token, _goreleaser_changelog_filters [EXTRACTED 1.00]
- **Local review flow (comment, render, resume agent)** — readme_diff_viewer, readme_conventional_comments, readme_local_review, claude_one_review_flow_two_sinks, readme_since_review_overlay [EXTRACTED 1.00]
- **Tabbed TUI surface (Threads, New, Clean, Settings)** — readme_tabbed_tui, readme_threads_tab, readme_new_tab, readme_clean_tab, readme_settings_tab [EXTRACTED 1.00]
- **Thread State Card Fields (state, blocked, pr, last, kind, next, recent)** — assets_demo_thread_detail_pane, assets_demo_state_column, assets_demo_blocked_field, assets_demo_pr_field, assets_demo_single_next_action, assets_demo_recent_done_list [EXTRACTED 1.00]
- **Command Center Navigation Surface (tabs, scope, filter, key hints)** — assets_demo_tab_bar, assets_demo_repo_scope, assets_demo_filter_prompt, assets_demo_keyhint_footer, assets_demo_thread_list [INFERRED 0.85]

## Communities (33 total, 2 thin omitted)

### Community 0 - "Diff View Rendering"
Cohesion: 0.06
Nodes (53): computeWordHighlights(), firstCodeRow(), formatGutter(), Cmd, Color, KeyMsg, Model, Msg (+45 more)

### Community 1 - "Claude Session Probing"
Cohesion: 0.07
Nodes (49): AgentState, tailRecord, assistantState(), Time, HasSession(), newestTranscript(), parseTail(), Probe() (+41 more)

### Community 2 - "Release and Project Docs"
Cohesion: 0.05
Nodes (49): GitHub Sponsors funding button, Bug report issue template, Roadmap contact link (issue chooser), Feature request issue template, HOMEBREW_TAP_TOKEN cross-repo secret, Tag-triggered release workflow, Changelog commit filters, Homebrew cask distribution (+41 more)

### Community 3 - "Create and Task Forms"
Cohesion: 0.08
Nodes (30): Form, taskRefOf(), filterTasks(), Cmd, Msg, Option, Theme, listTasksCmd() (+22 more)

### Community 4 - "Settings and Config Tests"
Cohesion: 0.11
Nodes (30): DefaultConfig(), T, TestAgentCommand(), TestAgentMergePreservesDefault(), TestHeadlessClaude(), OpenEditorValue(), boolStr(), Cmd (+22 more)

### Community 5 - "Prompt Template Rendering"
Cohesion: 0.11
Nodes (38): clickupWithoutToken(), DataFields(), EnsureUserTemplate(), ExtractBlocked(), ExtractDone(), ExtractGoal(), ExtractHint(), ExtractNext() (+30 more)

### Community 6 - "Git Worktree Operations"
Cohesion: 0.12
Nodes (31): RemoveOutcome, AddWorktreeFromRef(), branchAt(), BranchExists(), captureRun(), ChangedFiles(), CheckRemoteGone(), cmdOutput() (+23 more)

### Community 7 - "CLI Commands and Auth"
Cohesion: 0.12
Nodes (28): clickupStatus(), cmdAuth(), cmdInit(), detectDefaultBranch(), detectStack(), ghLogin(), githubStatus(), groupTUI() (+20 more)

### Community 8 - "YAML Config Editor"
Cohesion: 0.17
Nodes (19): setOrClear(), Editor, EditorCommand(), emptyDoc(), findKey(), Cmd, OpenEditor(), T (+11 more)

### Community 9 - "ClickUp API Client"
Cohesion: 0.18
Nodes (16): clickupLogin(), Option, nidOptions(), clickupGET(), ClickUpLists(), ClickUpSpaces(), ClickUpTeams(), clickupToken() (+8 more)

### Community 10 - "TUI App Shell"
Cohesion: 0.18
Nodes (18): helpFor(), mastheadWidth(), modelChoices(), NewApp(), newCreateFor(), orderBases(), providerFor(), renderHelp() (+10 more)

### Community 11 - "TUI Layout Helpers"
Cohesion: 0.18
Nodes (19): AgentConfig, T, TestHexRGB(), TestSealBackgroundLeavesBasicBg(), TestSealBackgroundPreservesIntentionalBg(), TestSealBackgroundStampsResets(), centerBlock(), centerScreen() (+11 more)

### Community 12 - "Session State Store"
Cohesion: 0.18
Nodes (9): CleanEmptyDirs(), Time, Load(), Path(), T, TestLoadCorruptSelfHeals(), TestSaveConcurrentStaysValid(), Session (+1 more)

### Community 13 - "Config Schema"
Cohesion: 0.18
Nodes (16): cmdTemplates(), resolvePRBase(), resolvePRTarget(), ClickUp, Config, ForbidRule, FormatRule, TasksConfig (+8 more)

### Community 14 - "Worktree Clean View"
Cohesion: 0.14
Nodes (12): Worktree, CheckDirty(), CheckMerged(), IsDirty(), revCount(), Cmd, Msg, newCleanModel() (+4 more)

### Community 15 - "Theme and Styles"
Cohesion: 0.17
Nodes (15): ccRows(), T, TestDashboardCommandCenterFitsPanel(), TestDashboardDetailFollowsCursor(), TestDashboardEmptyState(), ApplyTheme(), Avatar(), buildStyles() (+7 more)

### Community 16 - "Demo UI Surface"
Cohesion: 0.18
Nodes (17): Blocked Reason Field ("waiting on Stripe test keys"), Config Layers (Global · Repo personal · Repo shared, with ·inherited markers), Type-to-Filter Prompt (/fix narrows the thread list), Contextual Key-Hint Footer (cd · open · main · help), Nord Theme (dark muted palette in demo, theme = nord), PR Number Field (#38, #41, em-dash when none), Recent Done Checklist (what actually landed), norn Command Center Demo Recording (+9 more)

### Community 17 - "Local Review Rendering"
Cohesion: 0.24
Nodes (11): Key(), Render(), T, TestHeadingDefaultsToThought(), TestKeysAreUniqueAndCoverLabels(), TestRenderGroupsAndAnchors(), TestRenderSingularNoSummary(), TestWriteCreatesFile() (+3 more)

### Community 18 - "Claude Agent Runner"
Cohesion: 0.25
Nodes (12): envelope, Options, Result, Duration, EnrichBranchName(), Context, parseEnvelope(), Run() (+4 more)

### Community 19 - "Doctor Health Checks"
Cohesion: 0.23
Nodes (14): checkActiveRepo(), checkBinary(), checkDocsPaths(), checkGlobalConfig(), checkProjectConfigs(), checkStateFile(), cmdDoctor(), cmdRefreshDocs() (+6 more)

### Community 20 - "PR List View"
Cohesion: 0.20
Nodes (9): padPlain(), Cmd, Model, Msg, Time, NewPRList(), truncRight(), PRList (+1 more)

### Community 21 - "Direct Create and Review Entry"
Cohesion: 0.26
Nodes (12): aiResolveBranch(), clearScreen(), directCreate(), extractCreateFlags(), fetchReviewPR(), isAllDigits(), runApp(), runCreate() (+4 more)

### Community 22 - "Session Upsert and Branch Context"
Cohesion: 0.23
Nodes (12): cmdActivityTick(), cmdContext(), cmdProjectConfig(), currentBranch(), originRepoName(), readHintFromWorktreeMD(), resolveBranchBase(), upsertSession() (+4 more)

### Community 23 - "App Tab Routing"
Cohesion: 0.38
Nodes (6): checkRemote(), Cmd, Model, Msg, removeWorktreesCmd(), App

### Community 24 - "CLI Command Dispatch"
Cohesion: 0.20
Nodes (11): cmdDiffList(), cmdDiffPR(), cmdHelp(), cmdList(), cmdShellInit(), cmdStatus(), cmdTemplateEdit(), cmdTemplateNew() (+3 more)

### Community 25 - "Branch Naming"
Cohesion: 0.24
Nodes (9): Available(), T, TestBranchLacksSlug(), TestMakeBranch(), TestNormalizeSuggestedBranch(), BranchLacksSlug(), NormalizeSuggestedBranch(), createWorktree() (+1 more)

### Community 26 - "Local Review Flow Tests"
Cohesion: 0.51
Nodes (9): KeyMsg, T, key(), localDiffView(), send(), TestLocalCommentBodyKeys(), TestLocalCommentFlow(), TestLocalReviewNeedsContent() (+1 more)

### Community 27 - "Worktree Cd Picker"
Cohesion: 0.25
Nodes (6): Age(), Time, Cmd, Msg, newCdModel(), cdModel

### Community 28 - "Diff Command and Numstat"
Cohesion: 0.40
Nodes (6): cmdDiff(), gitOutput(), Model, handOffReview(), parseNumstat(), refExists()

### Community 30 - "Tasks Tab Tests"
Cohesion: 0.67
Nodes (3): T, TestTasksTabFitsPanel(), TestTasksTabNoProvider()

## Ambiguous Edges - Review These
- `Conventional-comment labels in review` → `Repo contribution conventions`  [AMBIGUOUS]
  README.md · relation: conceptually_related_to
- `Live State Indicator (working / idle / waiting for you)` → `PR Number Field (#38, #41, em-dash when none)`  [AMBIGUOUS]
  assets/demo.gif · relation: shares_data_with

## Knowledge Gaps
- **22 isolated node(s):** `record.sh script`, `github.com/sandbye/norn`, `envelope`, `ClickUp`, `worktreeCreatedMsg` (+17 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **2 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **What is the exact relationship between `Conventional-comment labels in review` and `Repo contribution conventions`?**
  _Edge tagged AMBIGUOUS (relation: conceptually_related_to) - confidence is low._
- **What is the exact relationship between `Live State Indicator (working / idle / waiting for you)` and `PR Number Field (#38, #41, em-dash when none)`?**
  _Edge tagged AMBIGUOUS (relation: shares_data_with) - confidence is low._
- **Why does `Config` connect `Config Schema` to `Claude Session Probing`, `Settings and Config Tests`, `Prompt Template Rendering`, `TUI App Shell`, `TUI Layout Helpers`, `Doctor Health Checks`, `Direct Create and Review Entry`, `Session Upsert and Branch Context`, `App Tab Routing`, `CLI Command Dispatch`, `Branch Naming`, `Diff Command and Numstat`?**
  _High betweenness centrality (0.252) - this node is a cross-community bridge._
- **Why does `DiffView` connect `Diff View Rendering` to `Local Review Rendering`, `Local Review Flow Tests`, `CLI Commands and Auth`?**
  _High betweenness centrality (0.111) - this node is a cross-community bridge._
- **Why does `Dashboard` connect `Claude Session Probing` to `Create and Task Forms`, `Session State Store`, `Config Schema`, `App Tab Routing`?**
  _High betweenness centrality (0.102) - this node is a cross-community bridge._
- **Are the 3 inferred relationships involving `main()` (e.g. with `RepoRoot()` and `SetTemplateDir()`) actually correct?**
  _`main()` has 3 INFERRED edges - model-reasoned connections that need verification._
- **What connects `record.sh script`, `github.com/sandbye/norn`, `envelope` to the rest of the system?**
  _22 weakly-connected nodes found - possible documentation gaps or missing edges._