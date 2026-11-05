import { useCustom } from "@refinedev/core";
import { List } from "@refinedev/antd";
import { Alert, App, Button, Descriptions, Drawer, Form, Input, Modal, Select, Space, Table, Tag, Typography } from "antd";
import { useState } from "react";
import axios from "axios";
import { consoleApiUrl } from "../../config";
import { useSessionState } from "../../hooks/useSessionState";
import { canResolve, resolutionPayload, reviewStatuses } from "./model";
import type { ReviewDecision, ReviewListing, ReviewPage, ReviewStatus } from "./model";

export const ServiceReviewList = () => {
  const { message } = App.useApp();
  const [paging, setPaging] = useSessionState<{ status: ReviewStatus; cursors: string[] }>("service-reviews.paging", { status: "review", cursors: ["0"] });
  const [selected, setSelected] = useState<ReviewListing>();
  const [deciding, setDeciding] = useState<ReviewListing>();
  const [submitting, setSubmitting] = useState(false);
  const [form] = Form.useForm<ReviewDecision>();
  const { query } = useCustom<ReviewPage>({
    url: `${consoleApiUrl}/service-reviews`, method: "get",
    config: { query: { status: paging.status, cursor: paging.cursors.at(-1), limit: 20 } },
  });
  const page = query.data?.data;

  const openDecision = (row: ReviewListing) => {
    form.resetFields();
    setDeciding(row);
  };
  const resolve = async () => {
    if (!deciding) return;
    let values: ReviewDecision;
    try { values = resolutionPayload(deciding.moderation, await form.validateFields()); }
    catch { return; }
    setSubmitting(true);
    try {
      const { data } = await axios.post(`${consoleApiUrl}/service-reviews/${deciding.moderation.id}/resolve`, values);
      if (data.code !== 0) throw new Error(data.msg || "Review decision failed");
      message.success(values.decision === "pass" ? "Approved. Publication will recheck the submitted version." : "Rejected. The seller will receive your guidance.");
      setDeciding(undefined);
      setSelected(undefined);
      await query.refetch();
    } catch (error) {
      const conflict = axios.isAxiosError(error) && error.response?.status === 409;
      message.error(conflict ? "This review changed or has already been resolved. The queue has been refreshed." : "Could not save the decision. Refresh the queue before retrying.");
      if (conflict) { setDeciding(undefined); setSelected(undefined); }
      await query.refetch();
    } finally { setSubmitting(false); }
  };
  const byteLimit = (max: number) => ({ validator: (_: unknown, value: string) =>
    !value || new TextEncoder().encode(value.trim()).length <= max ? Promise.resolve() : Promise.reject(new Error(`Maximum ${max} UTF-8 bytes`)) });

  return <>
    <List title="Service Review" headerButtons={<Space wrap>
      <Select aria-label="Review status" value={paging.status} style={{ width: 160 }} options={reviewStatuses.map(value => ({ value, label: value }))}
        onChange={status => { setPaging({ status, cursors: ["0"] }); setSelected(undefined); }} />
      <Button onClick={() => void query.refetch()} loading={query.isFetching}>Refresh</Button>
    </Space>}>
      {query.isError && <Alert type="error" showIcon message="Unable to load service reviews. Please retry." style={{ marginBottom: 16 }} />}
      <Table<ReviewListing> dataSource={query.isError ? [] : page?.items} loading={query.isFetching} rowKey={row => row.moderation.id} pagination={false} scroll={{ x: 950 }}>
        <Table.Column<ReviewListing> title="Submitted service" render={(_, row) => <Button type="link" onClick={() => setSelected(row)}>{String(row.listing.title || row.moderation.commission_id)}</Button>} />
        <Table.Column<ReviewListing> title="Service / seller ID" render={(_, row) => <><div>{row.moderation.commission_id}</div><Typography.Text type="secondary">{row.moderation.seller_agent_id}</Typography.Text></>} />
        <Table.Column<ReviewListing> title="Draft" render={(_, row) => row.moderation.draft_version} />
        <Table.Column<ReviewListing> title="Status" render={(_, row) => <Tag>{row.moderation.status}</Tag>} />
        <Table.Column<ReviewListing> title="Review guidance" render={(_, row) => <><div>{[row.moderation.flag, row.moderation.field].filter(Boolean).join(" · ")}</div>{row.moderation.reason}</>} />
        <Table.Column<ReviewListing> title="Submitted" render={(_, row) => new Date(row.moderation.created_at).toLocaleString()} />
        <Table.Column<ReviewListing> title="Actions" render={(_, row) => <Space><Button onClick={() => setSelected(row)}>Details</Button>{canResolve(row.moderation) && <Button type="primary" onClick={() => { setSelected(row); openDecision(row); }}>Decide</Button>}</Space>} />
      </Table>
      <Space style={{ marginTop: 16 }}>
        <Button disabled={paging.cursors.length === 1 || query.isFetching} onClick={() => setPaging({ ...paging, cursors: paging.cursors.slice(0, -1) })}>Previous</Button>
        <Typography.Text>Page {paging.cursors.length}</Typography.Text>
        <Button disabled={!page?.next_cursor || query.isFetching || query.isError} onClick={() => { if (page?.next_cursor) setPaging({ ...paging, cursors: [...paging.cursors, page.next_cursor] }); }}>Next</Button>
      </Space>
    </List>
    <Drawer title="Submitted service snapshot" open={!!selected} onClose={() => setSelected(undefined)} width={760}>
      {selected && <>
        <Descriptions column={1} bordered size="small" items={[
          { key: "id", label: "Review ID", children: selected.moderation.id },
          { key: "service", label: "Service ID", children: selected.moderation.commission_id },
          { key: "draft", label: "Submitted draft version", children: selected.moderation.draft_version },
          { key: "status", label: "Status", children: selected.moderation.status },
          { key: "reason", label: "Current seller guidance", children: selected.moderation.reason || "—" },
          { key: "model", label: "Original model outcome", children: [selected.moderation.model_decision, selected.moderation.model_flag, selected.moderation.model_field, selected.moderation.model_reason].filter(Boolean).join(" · ") || "—" },
          { key: "operator", label: "Recorded operator", children: selected.moderation.reviewer || "—" },
          { key: "decision", label: "Operator decision", children: selected.moderation.review_decision || "—" },
          { key: "audit", label: "Operator reason", children: selected.moderation.review_reason || "—" },
          { key: "at", label: "Reviewed at", children: selected.moderation.reviewed_at ? new Date(selected.moderation.reviewed_at).toLocaleString() : "—" },
        ]} />
        <Typography.Paragraph style={{ marginTop: 16 }}>This is the exact submitted snapshot. A later seller edit does not change this review.</Typography.Paragraph>
        <pre style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>{JSON.stringify(selected.listing, null, 2)}</pre>
        {canResolve(selected.moderation) && <Button type="primary" onClick={() => openDecision(selected)}>Approve or reject</Button>}
      </>}
    </Drawer>
    <Modal title="Resolve service review" open={!!deciding} onCancel={() => { if (!submitting) setDeciding(undefined); }} onOk={() => void resolve()} confirmLoading={submitting} okText="Submit decision" cancelButtonProps={{ disabled: submitting }} closable={!submitting} maskClosable={!submitting}>
      <Typography.Paragraph>{String(deciding?.listing.title || "")} · Draft {deciding?.moderation.draft_version}</Typography.Paragraph>
      <Typography.Paragraph type="secondary">Operator is your entered audit identity, not an authenticated username. Write the reason in the listing’s language.</Typography.Paragraph>
      <Form form={form} layout="vertical">
        <Form.Item name="decision" label="Decision" rules={[{ required: true }]}><Select options={[{ value: "pass", label: "Approve" }, { value: "reject", label: "Reject" }]} /></Form.Item>
        <Form.Item name="operator" label="Operator" rules={[{ required: true, whitespace: true }, byteLimit(200)]}><Input autoComplete="off" /></Form.Item>
        <Form.Item name="reason" label="Reason / seller guidance" rules={[{ required: true, whitespace: true }, byteLimit(8000)]}><Input.TextArea rows={4} /></Form.Item>
      </Form>
    </Modal>
  </>;
};
