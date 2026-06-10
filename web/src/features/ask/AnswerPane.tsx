export function AnswerPane({ answer, isStreaming }: { answer: string; isStreaming: boolean }) {
  return (
    <div className="min-h-24 whitespace-pre-wrap rounded-lg border p-4" data-testid="answer-pane">
      {answer}
      {isStreaming && <span className="ml-0.5 animate-pulse">▋</span>}
    </div>
  )
}
