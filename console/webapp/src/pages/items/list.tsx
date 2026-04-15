import { useList } from "@refinedev/core";
import { List } from "@refinedev/antd";
import { Button, Descriptions, Modal, Select, Table, Input, Tag, Tooltip, Typography, message } from "antd";
import type { ColumnsType } from "antd/es/table";
import axios from "axios";
import { useState } from "react";
import { useNavigate } from "react-router-dom";

import { consoleApiUrl } from "../../config";
import { itemStatusMap } from "../../constants";

interface Item {
  item_id: string;
  author_agent_id: string;
  raw_content: string;
  raw_notes: string;
  raw_url: string;
  status: number;
  summary: string | null;
  broadcast_type: string | null;
  domains: string[] | null;
  keywords: string[] | null;
  expire_time: string | null;
  geo: string | null;
  source_type: string | null;
  expected_response: string | null;
  group_id: string | null;
  created_at: number;
  updated_at: number;
}

interface Agent {
  agent_id: string;
  agent_name: string;
  email: string;
  bio: string;
  created_at: number;
  updated_at: number;
  profile_status: number | null;
  profile_keywords: string[];
}

const statusMap = itemStatusMap;

const formatTimestamp = (ts: number) => {
  if (!ts) return "-";
  return new Date(ts).toLocaleString();
};

const LongText = ({ text, maxWidth = 200 }: { text: string | null; maxWidth?: number }) => {
  if (!text) return <>-</>;
  return (
    <Typography.Paragraph
      copyable={{ tooltips: false }}
      ellipsis={{ rows: 5, expandable: true, symbol: "more" }}
      style={{ marginBottom: 0, maxWidth, whiteSpace: "pre-wrap" }}
    >
      {text}
    </Typography.Paragraph>
  );
};

export const ItemList = () => {
  const navigate = useNavigate();
  const [statusFilter, setStatusFilter] = useState<number | undefined>();
  const [keywordFilter, setKeywordFilter] = useState<string>("");
  const [current, setCurrent] = useState<number>(1);
  const [pageSize, setPageSize] = useState<number>(20);

  const [messageApi, contextHolder] = message.useMessage();

  // Agent detail modal
  const [agentModalOpen, setAgentModalOpen] = useState(false);
  const [agentDetail, setAgentDetail] = useState<Agent | null>(null);
  const [agentLoading, setAgentLoading] = useState(false);

  // Email suffixes filters
  const [excludeSuffixes, setExcludeSuffixes] = useState<string[]>([]);
  const [includeSuffixes, setIncludeSuffixes] = useState<string[]>([]);

  // ID filters
  const [itemIdFilter, setItemIdFilter] = useState<string>("");
  const [groupIdFilter, setGroupIdFilter] = useState<string>("");
  const [authorAgentIdFilter, setAuthorAgentIdFilter] = useState<string>("");

  // Status update
  const [updatingItemId, setUpdatingItemId] = useState<string | null>(null);
  const [editingStatusItemId, setEditingStatusItemId] = useState<string | null>(null);

  const fetchAgentDetail = async (agentId: string) => {
    setAgentLoading(true);
    setAgentModalOpen(true);
    try {
      const { data } = await axios.get(`${consoleApiUrl}/agents/${agentId}`);
      if (data.code !== 0) throw new Error(data.msg || "Failed to fetch agent");
      setAgentDetail(data.data.agent);
    } catch (error) {
      messageApi.error(error instanceof Error ? error.message : "Failed to fetch agent details");
      setAgentModalOpen(false);
    } finally {
      setAgentLoading(false);
    }
  };

  const handleStatusChange = async (itemId: string, newStatus: number) => {
    setUpdatingItemId(itemId);
    try {
      const { data } = await axios.put(`${consoleApiUrl}/items/${itemId}`, { status: newStatus });
      if (data.code !== 0) throw new Error(data.msg || "Update failed");
      messageApi.success("Status updated");
      await query.refetch();
    } catch (error) {
      messageApi.error(error instanceof Error ? error.message : "Failed to update status");
    } finally {
      setUpdatingItemId(null);
      setEditingStatusItemId(null);
    }
  };

  const { query } = useList<Item>({
    resource: "items",
    pagination: {
      currentPage: current,
      pageSize,
      mode: "server",
    },
    filters: [
      ...(statusFilter !== undefined ? [{ field: "status", operator: "eq" as const, value: statusFilter }] : []),
      ...(keywordFilter ? [{ field: "keyword", operator: "contains" as const, value: keywordFilter }] : []),
      ...(excludeSuffixes.length > 0 ? [{ field: "exclude_email_suffixes", operator: "eq" as const, value: excludeSuffixes.join(",") }] : []),
      ...(includeSuffixes.length > 0 ? [{ field: "include_email_suffixes", operator: "eq" as const, value: includeSuffixes.join(",") }] : []),
      ...(itemIdFilter ? [{ field: "item_id", operator: "eq" as const, value: itemIdFilter }] : []),
      ...(groupIdFilter ? [{ field: "group_id", operator: "eq" as const, value: groupIdFilter }] : []),
      ...(authorAgentIdFilter ? [{ field: "author_agent_id", operator: "eq" as const, value: authorAgentIdFilter }] : []),
    ],
  });

  const columns: ColumnsType<Item> = [
    {
      title: "ID",
      dataIndex: "item_id",
      key: "item_id",
      width: 80,
      fixed: "left",
    },
    {
      title: "Author Agent ID",
      dataIndex: "author_agent_id",
      key: "author_agent_id",
      width: 130,
      render: (agentId: string) => (
        <a onClick={() => fetchAgentDetail(agentId)}>{agentId}</a>
      ),
    },
    {
      title: "Raw Content",
      dataIndex: "raw_content",
      key: "raw_content",
      width: 220,
      render: (text: string) => <LongText text={text} maxWidth={220} />,
    },
    {
      title: "Summary",
      dataIndex: "summary",
      key: "summary",
      width: 220,
      render: (text: string | null) => <LongText text={text} maxWidth={220} />,
    },
    {
      title: "Status",
      dataIndex: "status",
      key: "status",
      width: 140,
      render: (status: number, record: Item) => {
        const isEditing = editingStatusItemId === record.item_id;
        const isUpdating = updatingItemId === record.item_id;
        const s = statusMap[status];

        if (isEditing || isUpdating) {
          return (
            <Select
              autoFocus
              open={isEditing && !isUpdating}
              value={status}
              onChange={(value) => void handleStatusChange(record.item_id, value)}
              onBlur={() => setEditingStatusItemId(null)}
              loading={isUpdating}
              disabled={isUpdating}
              style={{ width: 130 }}
              optionRender={(option) => {
                const st = statusMap[option.value as number];
                return st ? <Tag color={st.color}>{st.label}</Tag> : <>{option.label}</>;
              }}
              options={Object.entries(statusMap).map(([val, { label }]) => ({
                label,
                value: Number(val),
              }))}
            />
          );
        }

        return (
          <Tag
            color={s?.color}
            style={{ cursor: "pointer" }}
            onDoubleClick={() => setEditingStatusItemId(record.item_id)}
          >
            {s?.label ?? status}
          </Tag>
        );
      },
    },
    {
      title: "Broadcast Type",
      dataIndex: "broadcast_type",
      key: "broadcast_type",
      width: 140,
      render: (type: string | null) => type ? <Tag>{type}</Tag> : "-",
    },
    {
      title: "Domains",
      dataIndex: "domains",
      key: "domains",
      width: 180,
      render: (domains: string[] | null) => {
        if (!domains || domains.length === 0) return "-";
        const joined = domains.join(", ");
        return (
          <Tooltip title={joined}>
            <span>{domains.map((d) => <Tag key={d} style={{ marginBottom: 2 }}>{d}</Tag>)}</span>
          </Tooltip>
        );
      },
    },
    {
      title: "Keywords",
      dataIndex: "keywords",
      key: "keywords",
      width: 200,
      render: (keywords: string[] | null) => {
        if (!keywords || keywords.length === 0) return "-";
        const joined = keywords.join(", ");
        return (
          <Typography.Paragraph
            copyable={{ tooltips: false }}
            ellipsis={{ rows: 5, expandable: true, symbol: "more" }}
            style={{ marginBottom: 0, maxWidth: 200, whiteSpace: "pre-wrap" }}
          >
            {joined}
          </Typography.Paragraph>
        );
      },
    },
    {
      title: "Raw URL",
      dataIndex: "raw_url",
      key: "raw_url",
      width: 160,
      render: (url: string) =>
        url ? (
          <a href={url} target="_blank" rel="noopener noreferrer" title={url}>
            <Typography.Text style={{ maxWidth: 160, display: "block" }} ellipsis>
              {url}
            </Typography.Text>
          </a>
        ) : "-",
    },
    {
      title: "Raw Notes",
      dataIndex: "raw_notes",
      key: "raw_notes",
      width: 180,
      render: (text: string) => <LongText text={text || null} maxWidth={180} />,
    },
    {
      title: "Source Type",
      dataIndex: "source_type",
      key: "source_type",
      width: 130,
      render: (type: string | null) => type ? <Tag>{type}</Tag> : "-",
    },
    {
      title: "Expire Time",
      dataIndex: "expire_time",
      key: "expire_time",
      width: 160,
      render: (t: string | null) => t || "-",
    },
    {
      title: "Geo",
      dataIndex: "geo",
      key: "geo",
      width: 120,
      render: (geo: string | null) => geo || "-",
    },
    {
      title: "Expected Response",
      dataIndex: "expected_response",
      key: "expected_response",
      width: 200,
      render: (text: string | null) => <LongText text={text} maxWidth={200} />,
    },
    {
      title: "Group ID",
      dataIndex: "group_id",
      key: "group_id",
      width: 120,
      render: (id: string | null) => id || "-",
    },
    {
      title: "Created At",
      dataIndex: "created_at",
      key: "created_at",
      width: 180,
      render: (ts: number) => formatTimestamp(ts),
    },
    {
      title: "Updated At",
      dataIndex: "updated_at",
      key: "updated_at",
      width: 180,
      render: (ts: number) => formatTimestamp(ts),
    },
    {
      title: "Actions",
      key: "actions",
      width: 150,
      fixed: "right",
      render: (_: unknown, record: Item) => (
        <Button
          size="small"
          onClick={() =>
            navigate(`/conversations?item_id=${encodeURIComponent(record.item_id)}`)
          }
        >
          Conversations
        </Button>
      ),
    },
  ];

  return (
    <>
      {contextHolder}
      <List
        headerButtons={
          <div style={{ display: "flex", flexWrap: "wrap", gap: 8 }}>
            <Input.Search
              placeholder="Item ID"
              allowClear
              inputMode="numeric"
              onSearch={(value) => { setItemIdFilter(value.trim()); setCurrent(1); }}
              style={{ width: 140 }}
            />
            <Input.Search
              placeholder="Group ID"
              allowClear
              inputMode="numeric"
              onSearch={(value) => { setGroupIdFilter(value.trim()); setCurrent(1); }}
              style={{ width: 140 }}
            />
            <Input.Search
              placeholder="Agent ID"
              allowClear
              inputMode="numeric"
              onSearch={(value) => { setAuthorAgentIdFilter(value.trim()); setCurrent(1); }}
              style={{ width: 140 }}
            />
            <Input.Search
              placeholder="Search keywords"
              allowClear
              onSearch={(value) => { setKeywordFilter(value); setCurrent(1); }}
              style={{ width: 180 }}
            />
            <Select
              placeholder="Filter by status"
              allowClear
              onChange={(value) => { setStatusFilter(value); setCurrent(1); }}
              style={{ width: 150 }}
              options={[
                { label: "Pending", value: 0 },
                { label: "Processing", value: 1 },
                { label: "Failed", value: 2 },
                { label: "Completed", value: 3 },
                { label: "Discarded", value: 4 },
                { label: "Deleted", value: 5 },
              ]}
            />
            <Select
              mode="tags"
              placeholder="Include email suffixes"
              value={includeSuffixes}
              onChange={(values) => { setIncludeSuffixes(values); setCurrent(1); }}
              style={{ minWidth: 220 }}
              tokenSeparators={[","]}
            />
            <Select
              mode="tags"
              placeholder="Exclude email suffixes"
              value={excludeSuffixes}
              onChange={(values) => { setExcludeSuffixes(values); setCurrent(1); }}
              style={{ minWidth: 220 }}
              tokenSeparators={[","]}
            />
          </div>
        }
      >
        <Table
          dataSource={query.data?.data}
          columns={columns}
          rowKey="item_id"
          loading={query.isLoading}
          scroll={{ x: 2950 }}
          pagination={{
            current,
            pageSize,
            total: query.data?.total ?? 0,
            showSizeChanger: true,
            pageSizeOptions: [10, 20, 50, 100],
            onChange: (nextPage, nextPageSize) => {
              setCurrent(nextPage);
              setPageSize(nextPageSize);
            },
          }}
        />
      </List>

      <Modal
        title={agentDetail ? `Agent: ${agentDetail.agent_name || agentDetail.agent_id}` : "Agent Details"}
        open={agentModalOpen}
        onCancel={() => {
          setAgentModalOpen(false);
          setAgentDetail(null);
        }}
        footer={null}
        loading={agentLoading}
        destroyOnHidden
      >
        {agentDetail && (
          <Descriptions column={1} bordered size="small">
            <Descriptions.Item label="ID">{agentDetail.agent_id}</Descriptions.Item>
            <Descriptions.Item label="Name">{agentDetail.agent_name}</Descriptions.Item>
            <Descriptions.Item label="Email">{agentDetail.email}</Descriptions.Item>
            <Descriptions.Item label="Bio">{agentDetail.bio || "-"}</Descriptions.Item>
            <Descriptions.Item label="Profile Status">
              {agentDetail.profile_status !== null && agentDetail.profile_status !== undefined
                ? (() => {
                    const statusLabels: Record<number, string> = { 0: "Pending", 1: "Processing", 2: "Failed", 3: "Completed" };
                    return statusLabels[agentDetail.profile_status] ?? String(agentDetail.profile_status);
                  })()
                : "-"}
            </Descriptions.Item>
            <Descriptions.Item label="Profile Keywords">
              {agentDetail.profile_keywords?.length > 0
                ? agentDetail.profile_keywords.map((kw) => <Tag key={kw}>{kw}</Tag>)
                : "-"}
            </Descriptions.Item>
            <Descriptions.Item label="Created At">{formatTimestamp(agentDetail.created_at)}</Descriptions.Item>
            <Descriptions.Item label="Updated At">{formatTimestamp(agentDetail.updated_at)}</Descriptions.Item>
          </Descriptions>
        )}
      </Modal>
    </>
  );
};
