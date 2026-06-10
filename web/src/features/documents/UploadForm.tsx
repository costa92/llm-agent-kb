import { useState } from "react"
import { useMutation } from "@tanstack/react-query"
import { uploadDocument, type UploadInput } from "./api"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs"

// UploadForm posts a document (paste text or URL) and notifies onUploaded so
// the parent can refetch the list. Paste/url cover the M5b-tested paths; the
// URL tab maps to sourceType:"url".
export function UploadForm({ kbId, onUploaded }: { kbId: string; onUploaded: () => void }) {
  const [title, setTitle] = useState("")
  const [content, setContent] = useState("")
  const [url, setUrl] = useState("")
  const [mode, setMode] = useState<"paste" | "url">("paste")

  const mut = useMutation({
    mutationFn: () => {
      const input: UploadInput =
        mode === "paste"
          ? { title, sourceType: "paste", content }
          : { title, sourceType: "url", url }
      return uploadDocument(kbId, input)
    },
    onSuccess: () => {
      setTitle("")
      setContent("")
      setUrl("")
      onUploaded()
    },
  })

  const canSubmit = title.length > 0 && (mode === "paste" ? content.length > 0 : url.length > 0)

  return (
    <div className="space-y-3 rounded-lg border p-4">
      <div className="space-y-1">
        <Label htmlFor="doc-title">Title</Label>
        <Input id="doc-title" value={title} onChange={(e) => setTitle(e.target.value)} />
      </div>
      <Tabs value={mode} onValueChange={(v) => setMode(v as "paste" | "url")}>
        <TabsList>
          <TabsTrigger value="paste">Paste text</TabsTrigger>
          <TabsTrigger value="url">URL</TabsTrigger>
        </TabsList>
        <TabsContent value="paste" className="space-y-1">
          <Label htmlFor="doc-content">Content</Label>
          <Textarea id="doc-content" rows={6} value={content} onChange={(e) => setContent(e.target.value)} />
        </TabsContent>
        <TabsContent value="url" className="space-y-1">
          <Label htmlFor="doc-url">URL</Label>
          <Input id="doc-url" type="url" value={url} onChange={(e) => setUrl(e.target.value)} />
        </TabsContent>
      </Tabs>
      {mut.isError && <p className="text-sm text-destructive">{(mut.error as Error).message}</p>}
      <Button disabled={!canSubmit || mut.isPending} onClick={() => mut.mutate()}>
        {mut.isPending ? "Uploading…" : "Upload"}
      </Button>
    </div>
  )
}
