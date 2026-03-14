# Frontend Module Split (Manager)

Current frontend (`probe_manager/frontend/src`) has been split into layered modules:

## Structure

- `App.tsx`
  - App shell orchestration only (state composition + tab routing).
- `modules/app/components/*`
  - Presentational components:
  - `LoginPanel.tsx`
  - `Sidebar.tsx`
  - `OverviewTab.tsx`
  - `SystemSettingsTab.tsx`
  - `PlaceholderTab.tsx`
- `modules/app/services/controller-api.ts`
  - HTTP API calls to controller.
- `modules/app/hooks/*`
  - Business flow hooks:
  - `useAuthFlow.ts` (login/logout/challenge-response/private key status)
  - `useConnectionFlow.ts` (dashboard/admin status + websocket status stream)
  - `useUpgradeFlow.ts` (version checks + manager/controller upgrade flows)
  - `useLocalSettings.ts` (controller URL / upgrade project localStorage persistence)
- `modules/app/utils/url.ts`
  - URL normalization and websocket URL builder.
- `modules/app/authz.ts`
  - role/cert claim normalization + tab authorization map.
- `modules/app/types.ts`
  - shared frontend types.
- `modules/app/constants.ts`
  - storage keys/default project/tab constants.

## Goal

Keep `App.tsx` thin and move reusable business logic into hooks/services so each module is easier to test and evolve independently.
