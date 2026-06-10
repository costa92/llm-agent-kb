import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { MetricCards } from "./MetricCards"
import { DriftReportTable } from "./DriftReportTable"
import type { EvalResult, DriftView } from "@/lib/types"

describe("MetricCards", () => {
  it("renders retrieval metrics from a fixture", () => {
    const result: EvalResult = {
      kind: "retrieval",
      datasetName: "ds",
      retrieval: { precisionAtK: 0.8, recallAtK: 0.6, mrr: 0.75, groundingAtK: 0.9, examples: 10, topK: 5 },
    }
    render(<MetricCards result={result} />)
    expect(screen.getByText("Precision@K")).toBeInTheDocument()
    expect(screen.getByText("0.800")).toBeInTheDocument()
    expect(screen.getByText("0.750")).toBeInTheDocument() // MRR
  })

  it("renders triad generation means", () => {
    const result: EvalResult = {
      kind: "triad",
      datasetName: "ds",
      generation: { meanGroundedness: 0.85, meanAnswerRelevance: 0.92, examples: 5 },
    }
    render(<MetricCards result={result} />)
    expect(screen.getByText("Mean Groundedness")).toBeInTheDocument()
    expect(screen.getByText("0.850")).toBeInTheDocument()
  })
})

describe("DriftReportTable", () => {
  it("renders directions from a drift fixture", () => {
    const drift: DriftView = {
      dataset: "ds",
      deltas: [
        { name: "MRR", prev: 0.5, curr: 0.7, delta: 0.2, direction: "improved" },
        { name: "Precision", prev: 0.8, curr: 0.6, delta: -0.2, direction: "regressed" },
      ],
      histograms: [],
      newExamples: [],
      droppedExamples: [],
    }
    render(<DriftReportTable drift={drift} />)
    expect(screen.getByText("improved")).toBeInTheDocument()
    expect(screen.getByText("regressed")).toBeInTheDocument()
  })
})
