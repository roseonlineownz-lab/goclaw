import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}));

import { ProjectRoleChip } from "../components/project-role-chip";

describe("ProjectRoleChip", () => {
  it("renders three role options with radiogroup ARIA when interactive", () => {
    const handleChange = vi.fn();
    render(<ProjectRoleChip value="member" onChange={handleChange} />);
    const group = screen.getByRole("radiogroup");
    expect(group).toBeInTheDocument();
    const radios = screen.getAllByRole("radio");
    expect(radios).toHaveLength(3);
  });

  it("marks the current value as aria-checked", () => {
    render(<ProjectRoleChip value="editor" onChange={() => {}} />);
    const radios = screen.getAllByRole("radio");
    const checkedRoles = radios.filter((r) => r.getAttribute("aria-checked") === "true");
    expect(checkedRoles).toHaveLength(1);
    expect(checkedRoles[0]!.textContent).toMatch(/editor/i);
  });

  it("invokes onChange with the clicked role", async () => {
    const handleChange = vi.fn();
    const user = userEvent.setup();
    render(<ProjectRoleChip value="viewer" onChange={handleChange} />);
    const editor = screen.getByRole("radio", { name: /editor/i });
    await user.click(editor);
    expect(handleChange).toHaveBeenCalledWith("editor");
  });

  it("renders read-only group when no onChange provided", () => {
    render(<ProjectRoleChip value="member" />);
    expect(screen.queryByRole("radiogroup")).not.toBeInTheDocument();
    expect(screen.getByRole("group")).toBeInTheDocument();
  });
});
