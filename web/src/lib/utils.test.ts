import { describe, it, expect } from "vitest"
import { cn } from "./utils"

describe("cn", () => {
  it("merges and dedupes conflicting tailwind classes", () => {
    expect(cn("p-2", "p-4")).toBe("p-4")
    expect(cn("text-sm", "font-bold")).toBe("text-sm font-bold")
  })
})
