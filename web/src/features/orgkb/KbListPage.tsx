import { useState } from "react"
import { Link } from "@tanstack/react-router"
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query"
import { createOrg, createKb, listKbs } from "./api"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
  DialogFooter,
} from "@/components/ui/dialog"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"

// KbListPage takes an optional initialOrgId so tests can render the kb list
// without going through org creation. In the app, the route passes undefined
// and the user creates/selects an org first.
export function KbListPage({ initialOrgId }: { initialOrgId?: string }) {
  const qc = useQueryClient()
  const [orgId, setOrgId] = useState<string | undefined>(initialOrgId)
  const [orgName, setOrgName] = useState("")
  const [kbName, setKbName] = useState("")

  const orgMut = useMutation({
    mutationFn: () => createOrg(orgName),
    onSuccess: (org) => {
      setOrgId(org.id)
      setOrgName("")
    },
  })

  const kbsQuery = useQuery({
    queryKey: ["kbs", orgId],
    queryFn: () => listKbs(orgId!),
    enabled: !!orgId,
  })

  const kbMut = useMutation({
    mutationFn: () => createKb(orgId!, kbName),
    onSuccess: () => {
      setKbName("")
      void qc.invalidateQueries({ queryKey: ["kbs", orgId] })
    },
  })

  if (!orgId) {
    return (
      <div className="max-w-sm space-y-3">
        <h1 className="text-lg font-semibold">Create an organization</h1>
        <Input placeholder="Organization name" value={orgName} onChange={(e) => setOrgName(e.target.value)} />
        <Button disabled={!orgName || orgMut.isPending} onClick={() => orgMut.mutate()}>
          {orgMut.isPending ? "Creating…" : "Create organization"}
        </Button>
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-lg font-semibold">Knowledge bases</h1>
        <Dialog>
          <DialogTrigger asChild>
            <Button>New KB</Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader>
              <DialogTitle>Create knowledge base</DialogTitle>
            </DialogHeader>
            <Input placeholder="KB name" value={kbName} onChange={(e) => setKbName(e.target.value)} />
            <DialogFooter>
              <Button disabled={!kbName || kbMut.isPending} onClick={() => kbMut.mutate()}>
                {kbMut.isPending ? "Creating…" : "Create"}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>

      {kbsQuery.isLoading && <p className="text-sm text-muted-foreground">Loading…</p>}
      {kbsQuery.data && kbsQuery.data.items.length === 0 && (
        <p className="text-sm text-muted-foreground">No knowledge bases yet.</p>
      )}
      {kbsQuery.data && kbsQuery.data.items.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Namespace</TableHead>
              <TableHead className="text-right">Open</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {kbsQuery.data.items.map((kb) => (
              <TableRow key={kb.id}>
                <TableCell>{kb.name}</TableCell>
                <TableCell className="font-mono text-xs">{kb.namespace}</TableCell>
                <TableCell className="text-right">
                  {/* The /kb/$kbId/documents route is added in a later task, so it is
                      not yet in the generated route tree; cast the typed link props
                      until that route exists. */}
                  <Link
                    // eslint-disable-next-line @typescript-eslint/no-explicit-any
                    {...({ to: "/kb/$kbId/documents", params: { kbId: kb.id } } as any)}
                    className="text-sm underline"
                  >
                    Documents
                  </Link>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  )
}
