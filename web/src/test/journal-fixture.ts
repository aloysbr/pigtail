import type { LogEntry } from '@/api/types'

/**
 * A synthetic journal of 15 entries shaped like `pigtail subject log <id> --json` (kinds, steps, rounds and folds
 * as the daemon writes them), newest first. Times are chosen so in
 * `Europe/Paris` (UTC+1 after the 25 Oct switch to winter time) they read the displayed times: 15:38 … 13:55.
 */
export const JOURNAL_FIXTURE: LogEntry[] = [
  {
    id: "log_59b50005854955b9",
    subject_id: "sub_demo",
    ts: "2026-10-26T14:38:25.626Z",
    kind: "progress",
    text: "session-end step=write round=1 track=alpha · brouillon écrit depuis t3 ; périmètre face aux manques du capteur et réglage par défaut en attente",
  },
  {
    id: "log_2ec636dc41eaa3fb",
    subject_id: "sub_demo",
    ts: "2026-10-26T14:37:57.896Z",
    kind: "phase",
    text: "write saved → Projects/rain-gauge/draft.md (périmètre et réglage par défaut proposés, à confirmer)",
  },
  {
    id: "log_0c0035bc2fb42da8",
    subject_id: "sub_demo",
    ts: "2026-10-26T14:32:25.238Z",
    kind: "progress",
    text: "session-end step=sketch round=3 track=alpha · cadre pluviomètre, cible complète, boîtier mixte, teintes Sable + Ardoise ; croquis t3 validé comme référence ; passage au brouillon",
  },
  {
    id: "log_bf66764b3a71f2c5",
    subject_id: "sub_demo",
    ts: "2026-10-26T14:32:24.112Z",
    kind: "handoff",
    text: "# Handoff — Pilote pluviomètre (2026-10-26)\n\n**État** — alpha · étape sketch close (t3 validé) → prochaine étape **write** · statut designing.\n\n**Fait (session du 26/10)**\n- Survey : capteurs livrés mesurés, pilotes existants inventoriés → [[research]].\n- Pick : pilote dédié, croquer la cible complète.\n- Sketch t1 (4 boîtiers × 6 vues) → boîtier mixte ; t2 (6 teintes + curseurs) → deux teintes ; t3 = référence validée.\n\n**Prochaines étapes**\n1. Écrire le brouillon dans ce dossier (croquis: sketches/rain-gauge/t3/index.html).\n2. Plan + revue, puis implémentation dans un worktree.\n\n**Questions ouvertes** — aucune.",
  },
  {
    id: "log_d0c10cf6320c66f7",
    subject_id: "sub_demo",
    ts: "2026-10-26T14:31:53.220Z",
    kind: "phase",
    text: "sketch → t3 validée comme référence",
  },
  {
    id: "log_fd705f9a757b4af5",
    subject_id: "sub_demo",
    ts: "2026-10-26T14:28:25.988Z",
    kind: "progress",
    text: "session-end step=sketch round=3 track=alpha · pilote dédié, cible complète, boîtier mixte, teintes Sable + Ardoise, Ardoise gris pur ; croquis de référence t3 servi et vérifié, validation avant brouillon",
  },
  {
    id: "log_809433b445f3248b",
    subject_id: "sub_demo",
    ts: "2026-10-26T14:07:02.102Z",
    kind: "progress",
    text: "session-end step=sketch round=2 track=alpha · pilote dédié, cible complète, boîtier mixte ; croquis t2 (6 teintes + curseurs) servi et relu, choix de teinte en attente",
  },
  {
    id: "log_d94949640ed7344e",
    subject_id: "sub_demo",
    ts: "2026-10-26T13:45:56.696Z",
    kind: "progress",
    text: "session-end step=sketch round=1 track=alpha · pilote dédié, cible complète ; croquis t1 (4 boîtiers × 6 vues) servi et relu, choix du boîtier en attente",
  },
  {
    id: "log_171b6b525895a18d",
    subject_id: "sub_demo",
    ts: "2026-10-26T13:45:55.655Z",
    kind: "phase",
    text: "sketch t1 → servi et relu (25+12 captures, 9 défauts corrigés), choix du boîtier en attente",
  },
  {
    id: "log_5c6e7a161089255d",
    subject_id: "sub_demo",
    ts: "2026-10-26T13:45:55.207Z",
    kind: "status",
    text: "understanding → designing",
  },
  {
    id: "log_3136a05c05149109",
    subject_id: "sub_demo",
    ts: "2026-10-26T13:13:04.953Z",
    kind: "idea",
    text: "croquis dans sketches/rain-gauge/t<n>/, servis par pigtail serve",
  },
  {
    id: "log_06d289209481c7c8",
    subject_id: "sub_demo",
    ts: "2026-10-26T13:11:44.602Z",
    kind: "phase",
    text: "pick → pilote dédié, cible complète validée (go ahead)",
  },
  {
    id: "log_41fd9546f697f9b7",
    subject_id: "sub_demo",
    ts: "2026-10-26T13:02:25.488Z",
    kind: "phase",
    text: "survey → capteurs livrés mesurés, pilotes existants inventoriés, références de fiches techniques sourcées",
  },
  {
    id: "log_f473246a41b24554",
    subject_id: "sub_demo",
    ts: "2026-10-26T12:55:35.647Z",
    kind: "status",
    text: "inbox → understanding",
  },
  {
    id: "log_d90fd4939b99ab6b",
    subject_id: "sub_demo",
    ts: "2026-10-26T12:55:21.469Z",
    kind: "note",
    text: "workspace cmux 00000000-0000-4000-8000-000000000001 lié (auto)",
  },
]

/** The tz the fixture is read in. */
export const JOURNAL_TZ = 'Europe/Paris'

/** The fixture's « now »: 26 Oct 2026, 15:40 in Paris. */
export const JOURNAL_NOW = new Date('2026-10-26T14:40:00Z')
