import React from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Field, SegFilter, Tip } from "./primitives.jsx";

// SegFilter uses an option's n property for its count; test the required shape so counts cannot silently disappear.
describe("Segment Filter", () => {
  const options = [
    { id: "", label: "All", n: 12 },
    { id: "active", label: "Unprocessed", n: 3, color: "rose" },
    { id: "acked", label: "Acknowledged" }, // 无计数：不渲染，不能显示 "undefined"
  ];

  it("Displays the count on each item", () => {
    render(<SegFilter value="" onChange={() => {}} options={options} />);
    expect(screen.getByText("12")).toBeTruthy();
    expect(screen.getByText("3")).toBeTruthy();
  });

  it("Omits counts for items without one", () => {
    render(<SegFilter value="" onChange={() => {}} options={options} />);
    expect(screen.getByText("Acknowledged").textContent).toBe("Acknowledged");
    expect(screen.queryByText("undefined")).toBeNull();
  });

  // Zero is a valid count and must be displayed; a truthiness check would hide it.
  it("Displays a count of zero", () => {
    render(
      <SegFilter
        value=""
        onChange={() => {}}
        options={[{ id: "x", label: "Unprocessed", n: 0 }]}
      />,
    );
    expect(screen.getByText("0")).toBeTruthy();
  });

  it("Click returns the selected ID", async () => {
    const onChange = vi.fn();
    render(<SegFilter value="" onChange={onChange} options={options} />);
    await userEvent.click(screen.getByText("Unprocessed"));
    expect(onChange).toHaveBeenCalledWith("active");
  });
});

// Help behind ? can be opened and closed again.
describe("Field Help", () => {
  it("Opens and closes help with the ? button", async () => {
    render(
      <Field label="Gateway" help="Leave blank if no router is present">
        <input />
      </Field>,
    );
    expect(
      screen.queryByText("Leave blank if no router is present"),
    ).toBeNull();
    await userEvent.click(
      screen.getByRole("button", { name: "Help for Gateway" }),
    );
    expect(
      screen.getByText("Leave blank if no router is present"),
    ).toBeTruthy();
    await userEvent.click(
      screen.getByRole("button", { name: "Help for Gateway" }),
    );
    expect(
      screen.queryByText("Leave blank if no router is present"),
    ).toBeNull();
  });

  it("Does not render a help button without help text", () => {
    render(
      <Field label="Gateway" hint="Always-visible hint">
        <input />
      </Field>,
    );
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByText("Always-visible hint")).toBeTruthy();
  });
});

describe("Tip", () => {
  it("Escape dismisses help", async () => {
    render(
      <Tip
        label="Rate Limit Help"
        text="Limit bandwidth during business hours"
      />,
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Rate Limit Help" }),
    );
    expect(
      screen.getByText("Limit bandwidth during business hours"),
    ).toBeTruthy();
    await userEvent.keyboard("{Escape}");
    expect(
      screen.queryByText("Limit bandwidth during business hours"),
    ).toBeNull();
  });

  it("Portals help to body using viewport coordinates without clipping narrow screens", async () => {
    const vw = 375;
    const spy = vi
      .spyOn(document.documentElement, "clientWidth", "get")
      .mockReturnValue(vw);
    try {
      const { container } = render(
        <div style={{ overflow: "auto" }}>
          <Tip text="Limit bandwidth during business hours" />
        </div>,
      );
      const btn = screen.getByRole("button", { name: "Help" });
      btn.getBoundingClientRect = () => ({
        left: 350,
        right: 366,
        top: 100,
        bottom: 116,
        width: 16,
        height: 16,
      });
      await userEvent.click(btn);
      const pop = screen.getByText("Limit bandwidth during business hours");
      expect(container.contains(pop)).toBe(false);
      expect(pop.style.position).toBe("fixed");
      const left = parseInt(pop.style.left, 10),
        width = parseInt(pop.style.width, 10);
      expect(width).toBeLessThanOrEqual(vw - 32);
      expect(left).toBeGreaterThanOrEqual(16);
      expect(left + width).toBeLessThanOrEqual(vw - 16);
    } finally {
      spy.mockRestore();
    }
  });

  it("Closes help on scrolling to avoid misalignment", async () => {
    render(<Tip text="Limit bandwidth during business hours" />);
    await userEvent.click(screen.getByRole("button", { name: "Help" }));
    window.dispatchEvent(new Event("scroll"));
    await waitFor(() =>
      expect(
        screen.queryByText("Limit bandwidth during business hours"),
      ).toBeNull(),
    );
  });
});
