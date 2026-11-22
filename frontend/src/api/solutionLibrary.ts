import { apiClient } from './client'
import type { SelectionChannel } from './productSelections'

export interface SolutionSummary {
  id: string; name: string; purpose: string; parts: string[]; prerequisite: string; gap: string
  stage: string; delivery: string; priority: number; channels: SelectionChannel[]; types: string[]
  base_cost: number | null; target: number | null; incomplete: boolean; review: boolean; retired: boolean
}
export interface SolutionItem {
  id: string; name: string; role: string; kind: string; status: string; condition: string
  depends_on: string[]; offers: string[]
}
export interface SolutionProblem {
  trigger: string; check: string; action: string; fallback: string; component: string; status: string; offer?: string
}
export const journeyFields = ['purchase', 'activate', 'use', 'exception', 'renew'] as const
export const commercialFields = ['competition', 'information', 'acquisition', 'repeat', 'costs', 'aftercare'] as const
export const stages = ['priority', 'trial', 'review', 'hold'] as const
export const dispositions = ['component', 'backup', 'pending', 'unsuitable'] as const
export interface Solution {
  id: string; goal_key: string; path_key: string; name: string; purpose: string; customer: string
  difference: string; prerequisite: string; gap: string; stage: string; delivery: string; priority: number
  channels: SelectionChannel[]; target: number | null; observed: number | null
  journey: Record<typeof journeyFields[number], string>; commercial: Record<typeof commercialFields[number], string>
  problems: SolutionProblem[]; components: SolutionItem[]; evidence: string[]
}
export interface Offer {
  id: string; shop_id: string; shop: string; name: string; url: string; type: string; category: string; minimum: number
  cost: number | null; quote_at: string; detail_state: string; detail_at: string; original?: string
  detail_reason?: string; disposition: string; reason: string
}
export interface SolutionEvidence { id: string; kind: string; title: string; url: string; at: string; fact: string }
export interface SolutionDetail {
  solution: Solution; revision: number; version: string; analyzed_at: string; snapshot_at: string; notes: string
  conflicts: string[] | null; incoming?: Solution; offers: Offer[]; evidence: SolutionEvidence[]
}
export interface Availability { state: string; quote_at?: string; cost?: number }
export interface LibraryDetail { detail: SolutionDetail; availability: Record<string, Availability> }
export interface Audit {
  version: string; analyzed_at: string
  snapshot: { at: string; source: string; count: number; shops: number; sha256: string; identity_sha256: string }
  valid: number; incomplete: number; verified: number
  coverage: Array<{ type: string; total: number; uses: Record<string, number>; details: Record<string, number>; unpriced: number }>
  merges: Array<{ solution_id: string; variants: string; reason: string }>
}
const base = '/admin/public-account-import/solutions'
export async function listSolutions() {
  return (await apiClient.get<{ version: string; solutions: SolutionSummary[] }>(base)).data
}
export async function getSolution(id: string) {
  return (await apiClient.get<LibraryDetail>(base + '/' + encodeURIComponent(id))).data
}
export async function editSolution(solution: Solution, revision: number, notes: string, useIncoming: boolean) {
  return (await apiClient.put<{ revision: number }>(base + '/' + encodeURIComponent(solution.id),
    { solution, revision, notes, use_incoming: useIncoming })).data
}
export async function getSolutionAudit(version: string) {
  return (await apiClient.get<Audit>(base + '/reports/audit', { params: { version } })).data
}
export async function getSolutionOffers(version: string, type: string, disposition: string, page: number) {
  return (await apiClient.get<{ total: number; offers: Offer[] }>(base + '/reports/offers',
    { params: { version, type, disposition, page } })).data
}
