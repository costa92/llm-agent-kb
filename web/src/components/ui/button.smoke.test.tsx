import { describe, it, expect } from "vitest"
import { render, screen } from "@testing-library/react"
import { Button } from "./button"

describe("shadcn Button", () => {
  it("renders its children as a button", () => {
    render(<Button>Save</Button>)
    expect(screen.getByRole("button", { name: "Save" })).toBeInTheDocument()
  })
})
