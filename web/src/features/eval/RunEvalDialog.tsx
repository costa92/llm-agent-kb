import { useState } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { runEval, type RunEvalResponse } from "./api"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
  DialogFooter,
} from "@/components/ui/dialog"

type Kind = "retrieval" | "triad" | "global" | "drift"

export function RunEvalDialog({ kbId, onComplete }: { kbId: string; onComplete: (r: RunEvalResponse) => void }) {
  const qc = useQueryClient()
  const [kind, setKind] = useState<Kind>("retrieval")
  const [dataset, setDataset] = useState("")

  const mut = useMutation({
    mutationFn: () => runEval(kbId, kind, dataset),
    onSuccess: (r) => {
      onComplete(r)
      void qc.invalidateQueries({ queryKey: ["eval-runs", kbId] })
    },
  })

  return (
    <Dialog>
      <DialogTrigger asChild>
        <Button>Run eval</Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Run evaluation</DialogTitle>
        </DialogHeader>
        <Tabs value={kind} onValueChange={(v) => setKind(v as Kind)}>
          <TabsList>
            <TabsTrigger value="retrieval">Retrieval</TabsTrigger>
            <TabsTrigger value="triad">Triad</TabsTrigger>
            <TabsTrigger value="global">Global</TabsTrigger>
            <TabsTrigger value="drift">Drift</TabsTrigger>
          </TabsList>
        </Tabs>
        <Textarea
          rows={8}
          placeholder='Dataset JSONL — one example per line, e.g. {"question":"...","answer":"..."}'
          value={dataset}
          onChange={(e) => setDataset(e.target.value)}
        />
        {mut.isError && <p className="text-sm text-destructive">{(mut.error as Error).message}</p>}
        <DialogFooter>
          <Button disabled={!dataset || mut.isPending} onClick={() => mut.mutate()}>
            {mut.isPending ? "Running…" : "Run"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
