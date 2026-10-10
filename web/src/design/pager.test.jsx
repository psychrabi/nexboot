import React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Pager } from "./primitives.jsx";
import { usePaged } from "../lib/hooks.js";

describe("Pager", () => {
  it("Shows total, offers 10/20/50 rows per page, and calls page/size handlers", async () => {
    const onPage = vi.fn(),
      onSize = vi.fn();
    render(
      <Pager page={1} size={10} total={35} onPage={onPage} onSize={onSize} />,
    );
    expect(screen.getByText("Total 35 entries")).toBeTruthy();
    expect(screen.getByText("1 / 4")).toBeTruthy();
    const sizes = screen.getByLabelText("Rows per page");
    expect([...sizes.options].map((o) => o.value)).toEqual(["10", "20", "50"]);
    await userEvent.selectOptions(sizes, "20");
    expect(onSize).toHaveBeenCalledWith(20);
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(onPage).toHaveBeenCalledWith(2);
    expect(screen.getByRole("button", { name: "Previous" }).disabled).toBe(
      true,
    );
  });

  it("Hides pagination when content fits on the smallest page", () => {
    const { container } = render(
      <Pager
        page={1}
        size={10}
        total={10}
        onPage={() => {}}
        onSize={() => {}}
      />,
    );
    expect(container.textContent).toBe("");
  });

  it("Allows changing page size back even when all content fits", () => {
    render(
      <Pager
        page={1}
        size={50}
        total={30}
        onPage={() => {}}
        onSize={() => {}}
      />,
    );
    expect(screen.getByLabelText("Rows per page")).toBeTruthy();
  });
});

function List({ items }) {
  const paged = usePaged(items);
  return (
    <>
      <ul>
        {paged.rows.map((r) => (
          <li key={r}>{r}</li>
        ))}
      </ul>
      <Pager {...paged.pager} />
    </>
  );
}

describe("usePaged", () => {
  it("Defaults to ten rows and resets to page one after selecting twenty", async () => {
    const items = Array.from({ length: 25 }, (_, i) => `row-${i}`);
    render(<List items={items} />);
    expect(screen.getAllByRole("listitem")).toHaveLength(10);
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    expect(screen.getAllByRole("listitem")[0].textContent).toBe("row-10");
    await userEvent.selectOptions(screen.getByLabelText("Rows per page"), "20");
    expect(screen.getAllByRole("listitem")).toHaveLength(20);
    expect(screen.getAllByRole("listitem")[0].textContent).toBe("row-0");
  });
});
