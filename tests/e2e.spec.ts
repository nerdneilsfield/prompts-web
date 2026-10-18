import { expect, test, type Page } from "@playwright/test";

test.describe.configure({ mode: "serial" });

async function login(page: Page) {
  await page.goto("/");
  await page.getByLabel("站点密码").fill("secret");
  await page.getByRole("button", { name: "登录" }).click();
  await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible();
}

async function newPrompt(page: Page, title: string, body: string, tags: string[]) {
  await page.getByRole("button", { name: "＋ 新增 Prompt" }).click();
  const form = page.getByRole("form", { name: "新增 Prompt" });
  await form.getByLabel("标题").fill(title);
  await form.getByLabel("正文").fill(body);
  for (const t of tags) {
    await form.getByLabel("标签（可添加多个）").fill(t);
    await form.getByLabel("标签（可添加多个）").press("Enter");
  }
  await form.getByRole("button", { name: "保存" }).click();
  await expect(page.getByRole("heading", { name: title })).toBeVisible();
}

const tagBar = (page: Page) => page.getByLabel("标签筛选");
const cards = (page: Page) => page.getByRole("list").locator(".card");

test("登录失败与空库可直接新增", async ({ page }) => {
  await page.goto("/");
  await page.getByLabel("站点密码").fill("wrong");
  await page.getByRole("button", { name: "登录" }).click();
  await expect(page.getByText("密码不正确")).toBeVisible();
  await login(page);
  await expect(page.getByText("还没有 Prompt")).toBeVisible();
  await expect(page.getByRole("button", { name: "＋ 新增 Prompt" })).toBeVisible();
});

test("多 tag、筛选、搜索、复制、编辑 title/body/tags 并刷新验证", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await login(page);

  await newPrompt(page, "周报助手", "请根据 {{内容}} 写周报", ["tagA", "tagB"]);
  await newPrompt(page, "代码评审", "评审下面的代码：{{code}}", ["tagB"]);
  await newPrompt(page, "无标签草稿", "随手记", []);
  await expect(cards(page)).toHaveCount(3);

  // 详情 chips 与侧栏 tag 同步
  await expect(tagBar(page).getByRole("button", { name: /^tagA/ })).toBeVisible();
  await expect(tagBar(page).getByRole("button", { name: /^tagB 2/ })).toBeVisible();

  // 点 tag 分类筛选
  await tagBar(page).getByRole("button", { name: /^tagA/ }).click();
  await expect(cards(page)).toHaveCount(1);
  await expect(cards(page).first()).toContainText("周报助手");
  await expect(page.getByRole("status").filter({ hasText: "当前筛选" })).toContainText("标签「tagA」");

  // 切到 tagB：两条；再叠加关键词
  await tagBar(page).getByRole("button", { name: /^tagB/ }).click();
  await expect(cards(page)).toHaveCount(2);
  await page.getByLabel("搜索标题或正文").fill("评审");
  await expect(cards(page)).toHaveCount(1);
  await expect(cards(page).first()).toContainText("代码评审");
  await page.getByRole("button", { name: "清除筛选" }).click();
  await expect(cards(page)).toHaveCount(3);

  // 点列表卡片上的 chip 也能筛选
  await cards(page).filter({ hasText: "周报助手" }).getByRole("button", { name: "tagA" }).click();
  await expect(cards(page)).toHaveCount(1);
  await page.getByRole("button", { name: "清除筛选" }).click();

  // 复制：占位符原样
  await cards(page).filter({ hasText: "周报助手" }).click();
  await page.getByRole("button", { name: "复制正文" }).click();
  await expect(page.getByText("已复制正文")).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("请根据 {{内容}} 写周报");

  // 编辑：标题、正文、tags 一起改；去掉 tagA 加 tagC
  await page.getByRole("button", { name: "编辑" }).click();
  const form = page.getByRole("form", { name: "编辑 Prompt" });
  await expect(form.getByLabel("标题")).toHaveValue("周报助手");
  await expect(form.getByLabel("正文")).toHaveValue("请根据 {{内容}} 写周报");
  await expect(form.getByRole("button", { name: "移除标签 tagA" })).toBeVisible();
  await expect(form.getByRole("button", { name: "移除标签 tagB" })).toBeVisible();
  await form.getByLabel("标题").fill("周报助手 v2");
  await form.getByLabel("正文").fill("请用三段话根据 {{内容}} 写周报");
  await form.getByRole("button", { name: "移除标签 tagA" }).click();
  await form.getByLabel("标签（可添加多个）").fill("tagC");
  await form.getByLabel("标签（可添加多个）").press("Enter");
  await form.getByRole("button", { name: "保存" }).click();

  const detail = page.getByLabel("Prompt 详情");
  await expect(detail.getByRole("heading", { name: "周报助手 v2" })).toBeVisible();
  await expect(detail.getByRole("button", { name: "tagB" })).toBeVisible();
  await expect(detail.getByRole("button", { name: "tagC" })).toBeVisible();
  await expect(detail.getByRole("button", { name: "tagA" })).toHaveCount(0);
  await expect(page.getByTestId("prompt-body")).toHaveText("请用三段话根据 {{内容}} 写周报");
  // tag 选项立即同步：tagA 不再存在
  await expect(tagBar(page).getByRole("button", { name: /^tagA/ })).toHaveCount(0);
  await expect(tagBar(page).getByRole("button", { name: /^tagC/ })).toBeVisible();

  // 刷新后仍在（来自 DB）
  await page.reload();
  await expect(cards(page)).toHaveCount(3);
  await expect(cards(page).first()).toContainText("周报助手 v2");
  await expect(tagBar(page).getByRole("button", { name: /^tagA/ })).toHaveCount(0);
  await tagBar(page).getByRole("button", { name: /^tagC/ }).click();
  await expect(cards(page)).toHaveCount(1);
  await expect(cards(page).first()).toContainText("周报助手 v2");
  await cards(page).first().click();
  await expect(page.getByTestId("prompt-body")).toHaveText("请用三段话根据 {{内容}} 写周报");
  await page.getByRole("button", { name: "清除筛选" }).click();

  // 清空全部 tags 允许保存
  await cards(page).filter({ hasText: "周报助手 v2" }).click();
  await page.getByRole("button", { name: "编辑" }).click();
  const form2 = page.getByRole("form", { name: "编辑 Prompt" });
  await form2.getByRole("button", { name: "移除标签 tagB" }).click();
  await form2.getByRole("button", { name: "移除标签 tagC" }).click();
  await form2.getByRole("button", { name: "保存" }).click();
  await expect(detail.getByText("无标签")).toBeVisible();
  await expect(tagBar(page).getByRole("button", { name: /^tagC/ })).toHaveCount(0);
  await expect(tagBar(page).getByRole("button", { name: /^tagB 1/ })).toBeVisible();

  // 删除
  await page.once("dialog", (d) => d.accept());
  await page.getByRole("button", { name: "删除" }).click();
  await expect(cards(page)).toHaveCount(2);
});

async function addScheme(page: Page, name: string, body: string) {
  await page.getByRole("button", { name: "＋ 新建方案" }).click();
  const f = page.getByRole("form", { name: "方案表单" });
  await f.getByLabel("方案名称").fill(name);
  await f.getByLabel(/方案正文/).fill(body);
  await f.getByRole("button", { name: "保存方案" }).click();
  await expect(page.getByRole("button", { name: `编辑方案 ${name}` })).toBeVisible();
}

test("方案管理与优化：diff、变量警告、编辑另存、原文不变", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "优化工作台" }).click();
  await expect(page.getByText("还没有方案")).toBeVisible();
  await addScheme(page, "精简", "去掉冗余表达");
  await addScheme(page, "临时方案", "临时");

  // 方案编辑与删除
  await page.getByRole("button", { name: "编辑方案 临时方案" }).click();
  await page.getByRole("form", { name: "方案表单" }).getByLabel("方案名称").fill("临时方案2");
  await page.getByRole("button", { name: "保存方案" }).click();
  await expect(page.getByRole("button", { name: "编辑方案 临时方案2" })).toBeVisible();
  await page.once("dialog", (d) => d.accept());
  await page.getByRole("button", { name: "删除方案 临时方案2" }).click();
  await expect(page.getByRole("button", { name: "编辑方案 临时方案2" })).toHaveCount(0);

  const original = "你好 {{name}}，请总结 {{text}}";
  await page.getByLabel("原文").fill(original);
  await page.getByRole("button", { name: "开始优化" }).click();
  const result = page.getByLabel(/优化结果（可编辑）/);
  await expect(result).toHaveValue("优化后：你好 {{user}}，请总结 {{text}}");
  await expect(page.getByTestId("diff").locator("ins").first()).toBeVisible();
  const warn = page.getByTestId("placeholder-warning");
  await expect(warn).toContainText("丢失的变量");
  await expect(warn).toContainText("{{name}}");
  await expect(warn).toContainText("新增的变量");
  await expect(warn).toContainText("{{user}}");

  // 编辑结果：改回 {{name}} 后警告消失
  const edited = "你好 {{name}}，请总结 {{text}}（已改）";
  await result.fill(edited);
  await expect(warn).toHaveCount(0);

  // 另存
  const sa = page.getByRole("form", { name: "另存为新 Prompt" });
  await sa.getByLabel("标题").fill("优化版");
  await sa.getByLabel("标签（可添加多个）").fill("优化");
  await sa.getByLabel("标签（可添加多个）").press("Enter");
  await sa.getByRole("button", { name: "另存" }).click();
  await expect(sa.getByText("已另存为「优化版」")).toBeVisible();

  await page.getByRole("button", { name: "Prompt 库", exact: true }).first().click();
  await expect(cards(page).filter({ hasText: "优化版" })).toHaveCount(1);
  await expect(page.getByTestId("prompt-body")).toHaveText(edited);
  await page.getByRole("button", { name: "查看来源" }).click();
  const src = page.locator(".source-body");
  await expect(src).toContainText(original);
  await expect(src).toContainText("去掉冗余表达");
  await expect(src).toContainText("优化后：你好 {{user}}，请总结 {{text}}");

  // 原条目不变：还在，且不是被覆盖（无来源的两条仍存在）
  await expect(cards(page)).toHaveCount(3);
  // 来源条目的编辑不改动 source_run_id
  await page.getByRole("button", { name: "编辑" }).click();
  await page.getByRole("form", { name: "编辑 Prompt" }).getByLabel("标题").fill("优化版-改");
  await page.getByRole("button", { name: "保存" }).click();
  await expect(page.getByRole("button", { name: "查看来源" })).toBeVisible();
});

test("失败后输入保留并重试成功", async ({ page, request }) => {
  await login(page);
  await page.getByRole("button", { name: "优化工作台" }).click();
  await request.post("http://127.0.0.1:18081/__mode", { data: { fail: true } });
  await page.getByLabel("原文").fill("失败重试 {{a}}");
  await page.getByRole("button", { name: "开始优化" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "模型服务出错" })).toBeVisible();
  await expect(page.getByLabel("原文")).toHaveValue("失败重试 {{a}}");
  await expect(page.getByLabel("优化方案")).toHaveValue(/\d+/);

  await request.post("http://127.0.0.1:18081/__mode", { data: { fail: false } });
  await page.getByRole("button", { name: "重试优化" }).click();
  await expect(page.getByLabel(/优化结果（可编辑）/)).toHaveValue("优化后：失败重试 {{a}}");
  await expect(page.getByRole("alert").filter({ hasText: "模型服务出错" })).toHaveCount(0);
});

test("慢的旧请求在改输入或切方案后不污染当前状态", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "优化工作台" }).click();
  await addScheme(page, "第二方案", "另一种");

  // 改输入
  await page.getByLabel("原文").fill("SLOW 旧输入");
  await page.getByRole("button", { name: "开始优化" }).click();
  await expect(page.getByRole("button", { name: /优化中/ })).toBeDisabled();
  await page.getByLabel("原文").fill("新输入");
  await expect(page.getByRole("button", { name: "开始优化" })).toBeEnabled();
  await page.waitForTimeout(3500);
  await expect(page.getByLabel(/优化结果（可编辑）/)).toHaveCount(0);
  await expect(page.getByLabel("原文")).toHaveValue("新输入");

  // 先得到一个当前结果，再让慢请求被方案切换作废
  await page.getByRole("button", { name: "开始优化" }).click();
  await expect(page.getByLabel(/优化结果（可编辑）/)).toHaveValue("优化后：新输入");
  await page.getByLabel("原文").fill("SLOW 第二个");
  await page.getByRole("button", { name: "开始优化" }).click();
  await page.getByLabel("优化方案").selectOption({ label: "第二方案" });
  await page.waitForTimeout(3500);
  await expect(page.getByLabel(/优化结果（可编辑）/)).toHaveValue("优化后：新输入");
  await expect(page.getByLabel("原文")).toHaveValue("SLOW 第二个");
  await expect(page.getByRole("button", { name: "开始优化" })).toBeEnabled();
});

test("截图：桌面与窄屏", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "优化工作台" }).click();
  await page.getByLabel("原文").fill("你好 {{name}}，请总结 {{text}}");
  await page.getByRole("button", { name: "开始优化" }).click();
  await expect(page.getByLabel(/优化结果（可编辑）/)).toBeVisible();
  await page.screenshot({ path: "/tmp/prompts-web-workbench-desktop.png", fullPage: true });
  await page.getByRole("button", { name: "Prompt 库", exact: true }).first().click();
  await cards(page).first().click();
  await page.screenshot({ path: "/tmp/prompts-web-library-desktop.png", fullPage: true });

  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: "/tmp/prompts-web-library-mobile.png", fullPage: true });
  await page.getByRole("button", { name: "优化工作台" }).click();
  await page.screenshot({ path: "/tmp/prompts-web-workbench-mobile.png", fullPage: true });
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
  expect(overflow).toBeLessThanOrEqual(0);
});

// ---- 审阅修复回归 ----

const nav = (page: Page, name: string) => page.getByRole("navigation", { name: "主导航" }).getByRole("button", { name });

async function optimizeAndSave(page: Page, input: string, title: string) {
  if (await page.getByLabel("优化方案").isDisabled()) await addScheme(page, "回归方案", "保持原意");
  await page.getByLabel("原文").fill(input);
  await page.getByRole("button", { name: "开始优化" }).click();
  await expect(page.getByLabel(/优化结果（可编辑）/)).toHaveValue("优化后：" + input);
  const sa = page.getByRole("form", { name: "另存为新 Prompt" });
  await sa.getByLabel("标题").fill(title);
  await sa.getByRole("button", { name: "另存" }).click();
  await expect(sa.getByText(`已另存为「${title}」`)).toBeVisible();
}

test("库里未保存的表单不会被工作台另存冲掉", async ({ page }) => {
  await login(page);
  await page.getByRole("button", { name: "＋ 新增 Prompt" }).click();
  const form = page.getByRole("form", { name: "新增 Prompt" });
  await form.getByLabel("标题").fill("草稿标题");
  await form.getByLabel("正文").fill("草稿正文");
  await form.getByLabel("标签（可添加多个）").fill("draftTag");
  await form.getByLabel("标签（可添加多个）").press("Enter");

  await nav(page, "优化工作台").click();
  await optimizeAndSave(page, "保留草稿用 {{x}}", "来自工作台");

  await nav(page, "Prompt 库").click();
  await expect(form).toBeVisible();
  await expect(form.getByLabel("标题")).toHaveValue("草稿标题");
  await expect(form.getByLabel("正文")).toHaveValue("草稿正文");
  await expect(form.getByRole("button", { name: "移除标签 draftTag" })).toBeVisible();

  // 放弃草稿后才处理延后的新条目
  page.once("dialog", (d) => d.accept());
  await form.getByRole("button", { name: "取消" }).click();
  await expect(page.getByLabel("Prompt 详情").getByRole("heading", { name: "来自工作台" })).toBeVisible();
});

test("两个不同来源的 Prompt 不串来源内容", async ({ page }) => {
  await login(page);
  await nav(page, "优化工作台").click();
  await optimizeAndSave(page, "甲的原文", "来源甲");
  await optimizeAndSave(page, "乙的原文", "来源乙");

  await nav(page, "Prompt 库").click();
  await cards(page).filter({ hasText: "来源甲" }).click();
  await page.getByRole("button", { name: "查看来源" }).click();
  await expect(page.locator(".source-body")).toContainText("甲的原文");

  await cards(page).filter({ hasText: "来源乙" }).click();
  await expect(page.getByRole("heading", { name: "来源乙" })).toBeVisible();
  await expect(page.locator(".source-body")).toHaveCount(0);
  await page.getByRole("button", { name: "查看来源" }).click();
  await expect(page.locator(".source-body")).toContainText("乙的原文");
  await expect(page.locator(".source-body")).not.toContainText("甲的原文");
});

test("离开工作台即取消进行中的优化，迟到成功/失败都不污染", async ({ page, request }) => {
  await login(page);
  await nav(page, "优化工作台").click();
  if (await page.getByLabel("优化方案").isDisabled()) await addScheme(page, "回归方案", "保持原意");

  // 慢成功
  await page.getByLabel("原文").fill("SLOW 切页成功");
  await page.getByRole("button", { name: "开始优化" }).click();
  await expect(page.getByRole("button", { name: /优化中/ })).toBeDisabled();
  await nav(page, "Prompt 库").click();
  await page.waitForTimeout(3500);
  await nav(page, "优化工作台").click();
  await expect(page.getByLabel(/优化结果（可编辑）/)).toHaveCount(0);
  await expect(page.getByLabel("原文")).toHaveValue("SLOW 切页成功");
  // 被中止的请求可能已在服务端运行：不自动显示、不放开主按钮（否则新 ID 会重复计费），只能手动查询
  await expect(page.getByRole("button", { name: "开始优化" })).toBeDisabled();
  await page.getByRole("button", { name: "查询上次请求" }).click();
  // 服务端把被中止的请求记为已取消：明确失败终态，才释放 ID 允许重试
  await expect(page.getByRole("alert").filter({ hasText: "已被取消" })).toBeVisible();
  await expect(page.getByLabel(/优化结果（可编辑）/)).toHaveCount(0);
  await expect(page.getByRole("button", { name: "重试优化" })).toBeEnabled();

  // 慢失败
  await request.post("http://127.0.0.1:18081/__mode", { data: { fail: true } });
  try {
    await page.getByLabel("原文").fill("SLOW 切页失败");
    await page.getByRole("button", { name: "开始优化" }).click();
    await expect(page.getByRole("button", { name: /优化中/ })).toBeDisabled();
    await nav(page, "Prompt 库").click();
    await page.waitForTimeout(3500);
    await nav(page, "优化工作台").click();
    await expect(page.getByRole("alert").filter({ hasText: "模型服务出错" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "开始优化" })).toBeDisabled();
    await expect(page.getByLabel("原文")).toHaveValue("SLOW 切页失败");
    await page.getByRole("button", { name: "查询上次请求" }).click(); // 明确失败后才释放
    await expect(page.getByRole("alert").filter({ hasText: "已被取消" })).toBeVisible();
    await expect(page.getByRole("button", { name: "重试优化" })).toBeEnabled();
  } finally {
    await request.post("http://127.0.0.1:18081/__mode", { data: { fail: false } });
  }
});

test("会话失效后重新登录：搜索重载且保留未保存草稿", async ({ page, context }) => {
  await login(page);
  await newPrompt(page, "重登-代码评审", "评审代码", []);
  await newPrompt(page, "重登-别的", "无关内容", []);
  await page.getByRole("button", { name: "＋ 新增 Prompt" }).click();
  const form = page.getByRole("form", { name: "新增 Prompt" });
  await form.getByLabel("标题").fill("重登草稿");

  await context.clearCookies();
  await page.getByLabel("搜索标题或正文").fill("重登-代");
  await expect(page.getByText(/未保存的内容仍保留/)).toBeVisible();
  await page.getByLabel("站点密码").fill("secret");
  await page.getByRole("button", { name: "登录" }).click();

  await expect(cards(page)).toHaveCount(1);
  await expect(cards(page).first()).toContainText("重登-代码评审");
  await expect(page.getByRole("alert").filter({ hasText: "登录已失效" })).toHaveCount(0);
  await expect(form.getByLabel("标题")).toHaveValue("重登草稿");
});

test("方案初始加载遇 401，重新登录后恢复", async ({ page }) => {
  let failed = false;
  await page.route("**/api/schemes", async (route) => {
    if (!failed && route.request().method() === "GET") {
      failed = true;
      await route.fulfill({ status: 401, contentType: "application/json", body: JSON.stringify({ error: { code: "unauthorized", message: "x" } }) });
    } else await route.continue();
  });
  await page.goto("/");
  await page.getByLabel("站点密码").fill("secret");
  await page.getByRole("button", { name: "登录" }).click();
  await expect.poll(() => failed).toBe(true);
  await expect(page.getByText(/未保存的内容仍保留/)).toBeVisible();
  await page.getByLabel("站点密码").fill("secret");
  await page.getByRole("button", { name: "登录" }).click();

  await nav(page, "优化工作台").click();
  await expect(page.getByLabel("优化方案")).toHaveValue(/\d+/);
  await expect(page.getByRole("alert").filter({ hasText: "登录已失效" })).toHaveCount(0);
});

test("320/375/414/768 宽度：库与工作台无横向溢出、按钮不折行", async ({ page }) => {
  await login(page);
  await newPrompt(page, "响应式-标题很长很长很长很长很长很长很长很长很长很长很长很长很长", "正文".repeat(60), ["很长的标签名称很长很长", "b", "c"]);
  for (const width of [320, 375, 414, 768]) {
    await page.setViewportSize({ width, height: 900 });
    for (const name of ["Prompt 库", "优化工作台"]) {
      await nav(page, name).click();
      const m = await page.evaluate(() => ({
        over: document.documentElement.scrollWidth - window.innerWidth,
        wrapped: [...document.querySelectorAll("button")].filter((b) => b.offsetParent && b.getBoundingClientRect().height > 60).map((b) => b.textContent),
      }));
      expect(m, `${name} @${width}`).toEqual({ over: 0, wrapped: [] });
    }
  }
});

test("单条列表键盘聚焦的 ring 完整可见（不被 .cards 裁掉）", async ({ page }) => {
  await login(page);
  await newPrompt(page, "聚焦环唯一条目", "正文", []);
  await page.getByLabel("搜索标题或正文").fill("聚焦环唯一条目");
  await expect(cards(page)).toHaveCount(1);
  for (let i = 0; i < 20; i++) {
    await page.keyboard.press("Tab");
    if (await page.evaluate(() => document.activeElement?.classList.contains("card"))) break;
  }
  const g = await page.evaluate(() => {
    const el = document.activeElement as HTMLElement;
    const s = getComputedStyle(el);
    const r = el.getBoundingClientRect();
    const c = el.closest(".cards")!.getBoundingClientRect();
    const edge = parseFloat(s.outlineOffset) + parseFloat(s.outlineWidth); // outward reach of the ring
    return {
      isCard: el.classList.contains("card"),
      style: s.outlineStyle,
      left: r.left - edge - (c.left + 1), // 1px = .cards border
      right: c.right - 1 - (r.right + edge),
      top: r.top - edge - (c.top + 1),
      bottom: c.bottom - 1 - (r.bottom + edge),
    };
  });
  expect(g.isCard).toBe(true);
  expect(g.style).toBe("solid");
  for (const side of [g.left, g.right, g.top, g.bottom]) expect(side).toBeGreaterThanOrEqual(0);
});

test("320px：48 字符标签在编辑表单里 × 可见、可点、能清空并保存", async ({ page }) => {
  await login(page);
  await page.setViewportSize({ width: 320, height: 800 });
  const long = "abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuv"; // 48 chars, one word
  expect(long.length).toBe(48);
  await newPrompt(page, "长标签条目", "正文", [long]);

  // 详情 chip 和侧栏筛选 chip 不超出视口
  for (const loc of [tagBar(page).getByRole("button", { name: new RegExp("^" + long.slice(0, 10)) }), page.getByLabel("Prompt 详情").getByRole("button", { name: new RegExp("^" + long.slice(0, 10)) })]) {
    const box = (await loc.boundingBox())!;
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(320);
  }

  await page.getByRole("button", { name: "编辑" }).click();
  const form = page.getByRole("form", { name: "编辑 Prompt" });
  const remove = form.getByRole("button", { name: `移除标签 ${long}` });
  await expect(remove).toBeVisible();
  const box = (await remove.boundingBox())!;
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(320);
  // 完整标签文字可读（title）
  await expect(form.locator(".chip-label").first()).toHaveAttribute("title", long);

  await remove.click(); // 真实点击，不是 force
  await expect(remove).toHaveCount(0);
  await form.getByRole("button", { name: "保存" }).click();
  await expect(page.getByLabel("Prompt 详情").getByText("无标签")).toBeVisible();
  await page.reload();
  await cards(page).filter({ hasText: "长标签条目" }).click();
  await expect(page.getByLabel("Prompt 详情").getByText("无标签")).toBeVisible();
});

const callCount = async (request: import("@playwright/test").APIRequestContext) =>
  Number(await (await request.get("http://127.0.0.1:18081/__calls")).text());

test("丢响应后仍 running：保留同一请求 ID，手动查询找回结果，模型只调用一次", async ({ page, request }) => {
  await login(page);
  await nav(page, "优化工作台").click();
  if (await page.getByLabel("优化方案").isDisabled()) await addScheme(page, "恢复方案", "保持原意");

  // 第一次 POST：服务端照常收到并在途（模拟模型 3 秒后成功），但浏览器拿不到响应。
  let posts = 0;
  await page.route("**/api/optimizations", async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    posts++;
    void route.fetch().catch(() => {});
    await route.abort("connectionreset");
  });
  const before = await callCount(request);
  await page.getByLabel("原文").fill("SLOW 丢响应恢复");
  await page.getByRole("button", { name: "开始优化" }).click();

  // 自动补查一次仍 running：不能变成「重试」，必须保留并提供手动查询
  await expect(page.getByRole("alert").filter({ hasText: "服务端仍在处理" })).toBeVisible();
  await expect(page.getByRole("button", { name: "重试优化" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "开始优化" })).toBeDisabled();
  expect(posts).toBe(1);

  // 上游还没返回时手动查询：仍 running，继续保留
  await page.getByRole("button", { name: "查询上次请求" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "服务端仍在处理" })).toBeVisible();
  await expect(page.getByLabel(/优化结果（可编辑）/)).toHaveCount(0);

  await expect.poll(() => callCount(request), { timeout: 8000 }).toBe(before + 1);
  await page.waitForTimeout(3500); // 等上游返回并落库
  await page.getByRole("button", { name: "查询上次请求" }).click();
  await expect(page.getByLabel(/优化结果（可编辑）/)).toHaveValue("优化后：SLOW 丢响应恢复");
  expect(posts).toBe(1); // 没有再次 POST
  expect(await callCount(request)).toBe(before + 1); // 模型只被调用一次
  await expect(page.getByRole("button", { name: "查询上次请求" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "开始优化" })).toBeEnabled();
});

test("退出登录失败（503）保持已登录并可重试，成功后刷新仍未登录", async ({ page }) => {
  await login(page);
  await page.route("**/api/session", async (route) => {
    if (route.request().method() === "DELETE")
      await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: { code: "internal", message: "x" } }) });
    else await route.continue();
  });
  await page.getByRole("button", { name: "退出登录" }).click();
  await expect(page.getByText("退出登录失败，当前仍保持登录")).toBeVisible();
  await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible();
  await expect(page.getByLabel("站点密码")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "退出登录" })).toBeEnabled();
  await page.reload();
  await expect(page.getByRole("navigation", { name: "主导航" })).toBeVisible(); // 服务端会话确实还在

  await page.unroute("**/api/session");
  await page.getByRole("button", { name: "退出登录" }).click();
  await expect(page.getByLabel("站点密码")).toBeVisible();
  await page.reload();
  await expect(page.getByLabel("站点密码")).toBeVisible();
  await expect(page.getByRole("navigation", { name: "主导航" })).toHaveCount(0);
});

test("会话已在别处结束：退出得到真实 401，回到登录页且刷新仍未登录", async ({ page, context, playwright }) => {
  await login(page);
  const cookies = await context.cookies();
  // 另一个 HTTP client 带同一会话 cookie，真实结束服务端会话（不是伪造响应）
  const origin = new URL(page.url()).origin;
  const other = await playwright.request.newContext({
    baseURL: origin,
    extraHTTPHeaders: { Cookie: cookies.map((c) => `${c.name}=${c.value}`).join("; "), Origin: origin },
  });
  expect((await other.delete("/api/session")).ok()).toBe(true);
  expect((await other.get("/api/session").then((r) => r.json())).authenticated).toBe(false);
  await other.dispose();

  const del = page.waitForResponse((r) => r.url().endsWith("/api/session") && r.request().method() === "DELETE");
  await page.getByRole("button", { name: "退出登录" }).click();
  expect((await del).status()).toBe(401);
  await expect(page.getByLabel("站点密码")).toBeVisible();
  await expect(page.getByText("退出登录失败")).toHaveCount(0);
  await page.reload();
  await expect(page.getByLabel("站点密码")).toBeVisible();
});
