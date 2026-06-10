import { apiJSON } from "@/lib/apiClient"
import type { Kb, ListEnvelope, Org } from "@/lib/types"

export function createOrg(name: string): Promise<Org> {
  return apiJSON<Org>("/api/orgs", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name }),
  })
}

export function listKbs(orgId: string): Promise<ListEnvelope<Kb>> {
  return apiJSON<ListEnvelope<Kb>>(`/api/orgs/${orgId}/kbs`)
}

export function createKb(orgId: string, name: string): Promise<Kb> {
  return apiJSON<Kb>(`/api/orgs/${orgId}/kbs`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name }),
  })
}
