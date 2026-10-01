import { expect, test, type Page } from "@playwright/test";

async function classroom(page: Page) {
  await page.goto("/");
  const response = await page.request.post("/api/rooms", {
    data: { title: "Teacher workflow", kind: "classroom" },
  });
  expect(response.ok()).toBeTruthy();
  const { room, hostKey } = await response.json();
  await page.evaluate(
    ({ code, key }) => sessionStorage.setItem(`yuaction.host.${code}`, key),
    { code: room.code, key: hostKey },
  );
  const base = `/api/rooms/${room.code}`;
  for (const content of [
    "How does design thinking help users?",
    "How does design thinking help users today?",
    "An unrelated biology question",
  ]) {
    expect(
      (
        await page.request.post(`${base}/questions`, { data: { content } })
      ).ok(),
    ).toBeTruthy();
  }
  const current = await (await page.request.get(base)).json();
  return { base, code: room.code, key: hostKey, questions: current.questions };
}

test("unanswered stays visible, answered collapses by default, and manual groups survive live updates", async ({
  page,
  browser,
}) => {
  const { base, code, key, questions } = await classroom(page);
  const headers = { Authorization: `Bearer ${key}` };
  await page.request.patch(`${base}/questions/${questions[0].id}`, {
    headers,
    data: { status: "showing" },
  });
  await page.request.patch(`${base}/questions/${questions[1].id}`, {
    headers,
    data: { status: "answered" },
  });
  let groupPosts = 0;
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().endsWith("/question-groups"))
      groupPosts++;
  });
  await page.goto(`/rooms/${code}/host`);
  await expect(page.locator(".question-card")).toHaveCount(3);
  const unresolved = page.locator(".unresolved-questions");
  const answered = page.locator(".answered-questions");
  const answeredSummary = answered.locator(":scope > summary");
  await expect(unresolved.locator(".question-card")).toHaveCount(2);
  await expect(unresolved.locator(".question-card.showing")).toBeVisible();
  await expect(answeredSummary).toHaveText("已解答（1）");
  await expect(answered).not.toHaveAttribute("open");
  await expect(answered.locator(".question-card")).not.toBeVisible();
  await page.screenshot({
    path: "test-results/teacher-collapsed-desktop.png",
    fullPage: true,
  });
  await answeredSummary.focus();
  await page.keyboard.press("Enter");
  await expect(answered.locator(".question-card")).toBeVisible();
  await expect(answeredSummary).toBeFocused();
  await page.screenshot({
    path: "test-results/teacher-expanded-desktop.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: "test-results/teacher-expanded-mobile.png",
    fullPage: true,
  });
  await answeredSummary.click();
  await expect(answered.locator(".question-card")).not.toBeVisible();
  await page.screenshot({
    path: "test-results/teacher-collapsed-mobile.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1280, height: 720 });
  await expect(page.locator(".question-group")).toHaveCount(0);
  await expect(unresolved.locator(":scope > h4")).toHaveText("未解答（2）");
  const guestContext = await browser.newContext();
  const guest = await guestContext.newPage();
  const display = await guestContext.newPage();
  await guest.goto(`/rooms/${code}`);
  await display.goto(`/rooms/${code}/display`);
  await expect(
    display.getByRole("heading", { name: questions[0].content }),
  ).toBeVisible();
  await expect(
    guest.getByRole("button", { name: "整理相似问题", exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "整理相似问题", exact: true }).click();
  await expect(unresolved.locator(".question-group")).toHaveCount(1);
  await expect(page.locator(".question-group .question-card")).toHaveCount(2);
  await expect(page.locator(".question-card")).toHaveCount(3);
  await page.screenshot({
    path: "test-results/teacher-grouped-desktop.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: "test-results/teacher-grouped-mobile.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1280, height: 720 });
  await expect(
    display.getByRole("heading", { name: questions[0].content }),
  ).toBeVisible();
  expect(groupPosts).toBe(1);
  await page.request.post(`${base}/demo-segments`, {
    headers,
    data: {
      id: "teacher-caption",
      text: "A live subtitle update",
      translation: "字幕译文",
    },
  });
  await expect(page.locator(".caption-list")).toContainText(
    "A live subtitle update",
  );
  await expect(page.getByText(/问题列表已变更/)).toHaveCount(0);
  await guest
    .getByLabel("你的问题")
    .fill("How does design thinking help users?");
  await guest.getByRole("button", { name: "发送问题" }).click();
  await expect(page.locator(".question-card")).toHaveCount(4);
  await expect(page.locator(".question-group .question-card")).toHaveCount(2);
  await expect(page.getByText(/问题列表已变更/)).toBeVisible();
  expect(groupPosts).toBe(1);
  await expect(unresolved.locator(".question-card")).toHaveCount(3);
  await expect(unresolved.locator(".question-card.showing")).toHaveCount(1);
  await expect(unresolved.locator(".question-card.answered")).toHaveCount(0);
  await expect(answered.locator(".question-card")).not.toBeVisible();
  await page
    .getByRole("button", { name: "重新整理相似问题", exact: true })
    .click();
  await expect(
    unresolved.locator(".question-group .question-card"),
  ).toHaveCount(2);
  await expect(page.getByText(/问题列表已变更/)).toHaveCount(0);
  await page.screenshot({
    path: "test-results/teacher-grouped-desktop.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await answeredSummary.evaluate(
      (element) => element.getBoundingClientRect().height,
    ),
  ).toBeGreaterThanOrEqual(44);
  expect(
    await page
      .getByRole("button", { name: "重新整理相似问题", exact: true })
      .evaluate((element) => element.getBoundingClientRect().height),
  ).toBeGreaterThanOrEqual(44);
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBeTruthy();
  await page.screenshot({
    path: "test-results/teacher-grouped-mobile.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 1280, height: 720 });
  await page
    .locator(".question-card.showing")
    .getByRole("button", { name: "标记已解答" })
    .click();
  await expect(unresolved.locator(":scope > h4")).toHaveText("未解答（2）");
  await expect(unresolved.locator(".question-card")).toHaveCount(2);
  await expect(answeredSummary).toHaveText("已解答（2）");
  await expect(answered).not.toHaveAttribute("open");
  await expect(page.getByText(/问题列表已变更/)).toHaveCount(0);
  await answeredSummary.click();
  await expect(answered.locator(".question-card")).toHaveCount(2);
  await answered
    .locator(".question-card")
    .filter({ has: page.getByText(questions[0].content, { exact: true }) })
    .getByRole("button", { name: "移回待回应" })
    .click();
  await expect(unresolved.locator(".question-card")).toHaveCount(3);
  await expect(answeredSummary).toHaveText("已解答（1）");
  await expect(answered).toHaveAttribute("open");
  await expect(page.locator(".question-card")).toHaveCount(4);
  page.on("dialog", (d) => d.accept());
  await page
    .getByRole("button", {
      name: `删除问题：${questions[1].content}`,
      exact: true,
    })
    .click();
  await expect(page.locator(".question-card")).toHaveCount(3);
  await expect(
    page.locator(".question-card").filter({ hasText: questions[1].content }),
  ).toHaveCount(0);
  await page.reload();
  await expect(page.locator(".question-card")).toHaveCount(3);
  await expect(answered).not.toHaveAttribute("open");
  await expect(unresolved.locator(".question-card")).toHaveCount(3);
  await expect(page.getByText(/问题列表已变更/)).toBeVisible();
  await page.getByRole("button", { name: "按时间查看", exact: true }).click();
  await expect(page.locator(".question-group")).toHaveCount(0);
  await expect(page.locator(".question-card")).toHaveCount(3);
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBeTruthy();
  await page.screenshot({
    path: "test-results/teacher-unresolved-mobile.png",
    fullPage: true,
  });
  await guestContext.close();
});

test("repeat clicks, failed grouping and a lost response retain questions and recover saved groups", async ({
  page,
}) => {
  const { base, code, key } = await classroom(page);
  await page.goto(`/rooms/${code}/host`);
  await expect(page.locator(".question-card")).toHaveCount(3);
  let posts = 0;
  let mode: "lost" | "fail" | "normal" = "lost";
  let entered!: () => void, release!: () => void;
  const received = new Promise<void>((resolve) => {
    entered = resolve;
  });
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route("**/assistant/question-groups", async (route) => {
    if (route.request().method() !== "POST") {
      await route.continue();
      return;
    }
    posts++;
    if (mode === "lost") {
      await route.fetch(); // Server commits, browser never receives that response.
      entered();
      await gate;
      await route.abort("failed");
    } else if (mode === "fail") {
      await route.fulfill({
        status: 500,
        contentType: "application/json",
        body: JSON.stringify({ error: "Fixture grouping failure" }),
      });
    } else {
      await route.continue();
    }
  });
  const group = page.getByRole("button", { name: "整理相似问题", exact: true });
  await group.click();
  await received;
  await expect(
    page.getByRole("button", { name: "正在整理…", exact: true }),
  ).toBeDisabled();
  // Programmatic rapid second click also cannot bypass the synchronous guard.
  await page
    .getByRole("button", { name: "正在整理…", exact: true })
    .evaluate((button: HTMLButtonElement) => button.click());
  expect(posts).toBe(1);
  release();
  await expect(page.getByRole("alert")).toContainText("可重试");
  await expect(page.locator(".question-card")).toHaveCount(3);
  await page.reload();
  await expect(page.locator(".question-group")).toHaveCount(1);
  const saved = await (
    await page.request.get(`${base}/assistant/question-groups`, {
      headers: { Authorization: `Bearer ${key}` },
    })
  ).json();
  mode = "fail";
  await page
    .getByRole("button", { name: "重新整理相似问题", exact: true })
    .click();
  await expect(page.getByRole("alert")).toContainText(
    "Fixture grouping failure",
  );
  await expect(page.locator(".question-group")).toHaveCount(1);
  await expect(page.locator(".question-card")).toHaveCount(3);
  mode = "normal";
  await page
    .getByRole("button", { name: "重新整理相似问题", exact: true })
    .click();
  await expect(page.getByRole("alert")).toHaveCount(0);
  const retried = await (
    await page.request.get(`${base}/assistant/question-groups`, {
      headers: { Authorization: `Bearer ${key}` },
    })
  ).json();
  expect(retried).toEqual(saved);
});
