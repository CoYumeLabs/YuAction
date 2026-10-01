import { useEffect, useRef, useState, type ReactNode } from "react";
import { api, isUnresolvedQuestion, type Question } from "../api";
import { Button, Empty, ErrorNote } from "./ui";

type Grouping = {
  questionIds: string[];
  groups: { questionIds: string[] }[];
  stale: boolean;
  createdAt: string;
};
type Result = { grouping: Grouping | null };

export default function QuestionGroups({
  code,
  hostKey,
  questions,
  visibleQuestions,
  answeredQuestions,
  renderQuestion,
}: {
  code: string;
  hostKey: string;
  questions: Question[];
  visibleQuestions: Question[];
  answeredQuestions: Question[];
  renderQuestion: (q: Question) => ReactNode;
}) {
  const [grouping, setGrouping] = useState<Grouping | null>(null);
  const [grouped, setGrouped] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const request = useRef<AbortController | null>(null);
  const loading = useRef<AbortController | null>(null);
  const base = `/rooms/${code}/assistant/question-groups`;
  useEffect(() => {
    const controller = new AbortController();
    loading.current = controller;
    request.current = null;
    setBusy(false);
    setGrouping(null);
    setError("");
    void api<Result>(base, {
      key: hostKey,
      signal: AbortSignal.any([controller.signal, AbortSignal.timeout(15000)]),
    })
      .then((value) => {
        if (!controller.signal.aborted) setGrouping(value.grouping);
      })
      .catch((e: Error) => {
        if (!controller.signal.aborted) setError(e.message);
      });
    return () => {
      controller.abort();
      request.current?.abort();
    };
  }, [base, hostKey]);

  async function regroup() {
    if (request.current) return;
    loading.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    try {
      const result = await api<Result>(base, {
        method: "POST",
        key: hostKey,
        signal: AbortSignal.any([
          controller.signal,
          AbortSignal.timeout(15000),
        ]),
      });
      if (!controller.signal.aborted) {
        setGrouping(result.grouping);
        setGrouped(true);
      }
    } catch (e) {
      if (!controller.signal.aborted)
        setError(`${(e as Error).message}。原问题与上次分组保留，可重试。`);
    } finally {
      if (!controller.signal.aborted) setBusy(false);
      if (request.current === controller) request.current = null;
    }
  }

  const currentIDs = new Set(questions.map((q) => q.id));
  const stale =
    grouping &&
    (grouping.stale ||
      grouping.questionIds.length !== currentIDs.size ||
      grouping.questionIds.some((id) => !currentIDs.has(id)));
  // Always render current room objects: saved IDs cannot resurrect deletions,
  // duplicate cards, hide newcomers or overwrite answered/display states.
  function renderList(items: Question[]) {
    const visible = new Map(items.map((q) => [q.id, q]));
    const placed = new Set<string>();
    const groups =
      grouped && grouping
        ? grouping.groups
            .slice()
            .reverse()
            .map((group) => {
              const members = group.questionIds.filter((id) =>
                currentIDs.has(id),
              );
              const cards: Question[] = [];
              for (const id of members.slice().reverse()) {
                const q = visible.get(id);
                if (q && !placed.has(id)) {
                  cards.push(q);
                  placed.add(id);
                }
              }
              return { members, cards };
            })
            .filter((group) => group.cards.length)
        : [];
    const remaining = items.filter((q) => !placed.has(q.id));
    return (
      <div className="question-list">
        {groups.map((group, i) => (
          <section className="question-group" key={group.members.join(":")}>
            <h4>
              相似问题 {i + 1} · {group.cards.length} 条 ·{" "}
              {
                questions.filter(
                  (q) =>
                    group.members.includes(q.id) && isUnresolvedQuestion(q),
                ).length
              }{" "}
              条未解答
            </h4>
            {group.cards.map(renderQuestion)}
          </section>
        ))}
        {!!groups.length && !!remaining.length && <h4>未分组问题</h4>}
        {remaining.map(renderQuestion)}
      </div>
    );
  }

  return (
    <>
      <div className="grouping-toolbar">
        <Button
          className="soft small"
          disabled={busy || questions.length < 2}
          onClick={() => void regroup()}
        >
          {busy ? "正在整理…" : grouping ? "重新整理相似问题" : "整理相似问题"}
        </Button>
        {grouping && (
          <Button
            className="text small"
            aria-pressed={grouped}
            onClick={() => setGrouped(!grouped)}
          >
            {grouped ? "按时间查看" : "查看已整理分组"}
          </Button>
        )}
      </div>
      <p className="form-note">
        仅手动按文字相似度整理，不调用 AI。每条问题仍需单独回应。
      </p>
      <ErrorNote message={error} />
      {grouping && (
        <p className="form-note" role="status">
          上次整理覆盖 {grouping.questionIds.length} 条问题 ·{" "}
          {grouping.groups.length} 个相似分组。
          {stale &&
            " 问题列表已变更；新问题保留在未分组列表，已删除问题不会显示。可重新整理。"}
          {!grouping.groups.length && " 暂未找到文字相似的问题。"}
        </p>
      )}
      <section className="unresolved-questions" aria-label="未解答问题">
        <h4>未解答（{visibleQuestions.length}）</h4>
        {renderList(visibleQuestions)}
        {!visibleQuestions.length && (
          <Empty title="暂无未解答问题">
            新问题会实时出现在这里；AI 草稿不代表已解答。
          </Empty>
        )}
      </section>
      <details className="answered-questions">
        <summary>已解答（{answeredQuestions.length}）</summary>
        {renderList(answeredQuestions)}
      </details>
    </>
  );
}
