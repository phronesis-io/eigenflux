import { useList } from "@refinedev/core";
import { List } from "@refinedev/antd";
import { Button, Drawer, Empty, Input, Space, Spin, Table, Tag, Typography, message } from "antd";
import type { ColumnsType } from "antd/es/table";
import axios from "axios";
import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";

import { consoleApiUrl } from "../../config";

interface Conversation {
  conv_id: string;
  participant_a: string;
  participant_b: string;
  participant_a_name: string;
  participant_b_name: string;
  origin_type: string;
  origin_id?: string;
  last_sender_id: string;
  last_sender_name: string;
  msg_count: number;
  status: number;
  updated_at: number;
}

interface ConvMessage {
  msg_id: string;
  conv_id: string;
  sender_id: string;
  sender_name: string;
  content: string;
  created_at: number;
}

const formatTimestamp = (ts: number) => (ts ? new Date(ts).toLocaleString() : "-");

// Pastel palette for message backgrounds, one color per distinct sender.
const SENDER_PALETTE = [
  "#E6F4FF",
  "#F9F0FF",
  "#FFF7E6",
  "#F6FFED",
  "#FFF1F0",
  "#FFFBE6",
];

const colorForSender = (senderID: string) => {
  let hash = 0;
  for (let i = 0; i < senderID.length; i++) {
    hash = (hash * 31 + senderID.charCodeAt(i)) >>> 0;
  }
  return SENDER_PALETTE[hash % SENDER_PALETTE.length];
};

const originTypeLabel = (t: string) => {
  switch (t) {
    case "broadcast":
      return <Tag color="blue">Item</Tag>;
    case "friend":
      return <Tag color="green">Friend</Tag>;
    default:
      return <Tag>{t || "-"}</Tag>;
  }
};

const participantLabel = (id: string, name: string) =>
  name ? `${name} (${id})` : id;

export const ConversationList = () => {
  const [searchParams, setSearchParams] = useSearchParams();
  const [itemIdFilter, setItemIdFilter] = useState<string>(
    () => searchParams.get("item_id")?.trim() ?? "",
  );
  const [agentIdFilter, setAgentIdFilter] = useState<string>(
    () => searchParams.get("agent_id")?.trim() ?? "",
  );
  const [current, setCurrent] = useState<number>(1);
  const [pageSize, setPageSize] = useState<number>(20);

  const [messageApi, contextHolder] = message.useMessage();

  const [drawerOpen, setDrawerOpen] = useState(false);
  const [drawerConv, setDrawerConv] = useState<Conversation | null>(null);
  const [messages, setMessages] = useState<ConvMessage[]>([]);
  const [messagesLoading, setMessagesLoading] = useState(false);

  const hasFilter = !!itemIdFilter || !!agentIdFilter;

  const { query } = useList<Conversation>({
    resource: "conversations",
    pagination: {
      currentPage: current,
      pageSize,
      mode: "server",
    },
    filters: [
      ...(itemIdFilter ? [{ field: "item_id", operator: "eq" as const, value: itemIdFilter }] : []),
      ...(agentIdFilter ? [{ field: "agent_id", operator: "eq" as const, value: agentIdFilter }] : []),
    ],
    queryOptions: {
      enabled: hasFilter,
    },
  });

  const openMessagesDrawer = async (conv: Conversation) => {
    setDrawerConv(conv);
    setDrawerOpen(true);
    setMessages([]);
    setMessagesLoading(true);
    try {
      const { data } = await axios.get(`${consoleApiUrl}/conversations/${conv.conv_id}/messages`);
      if (data.code !== 0 || !data.data) throw new Error(data.msg || "Failed to load messages");
      setMessages(data.data.messages ?? []);
    } catch (error) {
      messageApi.error(error instanceof Error ? error.message : "Failed to load messages");
    } finally {
      setMessagesLoading(false);
    }
  };

  const closeDrawer = () => {
    setDrawerOpen(false);
    setDrawerConv(null);
    setMessages([]);
  };

  useEffect(() => {
    setCurrent(1);
  }, [itemIdFilter, agentIdFilter]);

  // Sync state from URL on navigation (e.g. clicking Conversations from Agents/Items page).
  useEffect(() => {
    const urlItemID = searchParams.get("item_id")?.trim() ?? "";
    const urlAgentID = searchParams.get("agent_id")?.trim() ?? "";
    setItemIdFilter((prev) => (prev === urlItemID ? prev : urlItemID));
    setAgentIdFilter((prev) => (prev === urlAgentID ? prev : urlAgentID));
  }, [searchParams]);

  // Sync state back to the URL so the current filter is shareable / reloadable.
  const applyItemIdFilter = (value: string) => {
    const next = value.trim();
    setItemIdFilter(next);
    const params = new URLSearchParams(searchParams);
    if (next) params.set("item_id", next);
    else params.delete("item_id");
    setSearchParams(params, { replace: true });
  };

  const applyAgentIdFilter = (value: string) => {
    const next = value.trim();
    setAgentIdFilter(next);
    const params = new URLSearchParams(searchParams);
    if (next) params.set("agent_id", next);
    else params.delete("agent_id");
    setSearchParams(params, { replace: true });
  };

  const columns: ColumnsType<Conversation> = [
    {
      title: "Participants",
      key: "participants",
      width: 280,
      render: (_: unknown, r: Conversation) => (
        <Space direction="vertical" size={2}>
          <span>{participantLabel(r.participant_a, r.participant_a_name)}</span>
          <span>{participantLabel(r.participant_b, r.participant_b_name)}</span>
        </Space>
      ),
    },
    {
      title: "Type",
      key: "origin_type",
      dataIndex: "origin_type",
      width: 100,
      render: (t: string) => originTypeLabel(t),
    },
    {
      title: "Item ID",
      dataIndex: "origin_id",
      key: "origin_id",
      width: 140,
      render: (_: unknown, r: Conversation) =>
        r.origin_type === "broadcast" && r.origin_id ? r.origin_id : "-",
    },
    {
      title: "Last Msg Time",
      dataIndex: "updated_at",
      key: "updated_at",
      width: 180,
      render: (ts: number) => formatTimestamp(ts),
    },
    {
      title: "Last Sender",
      key: "last_sender",
      width: 200,
      render: (_: unknown, r: Conversation) =>
        r.last_sender_id ? participantLabel(r.last_sender_id, r.last_sender_name) : "-",
    },
    {
      title: "Messages",
      dataIndex: "msg_count",
      key: "msg_count",
      width: 90,
    },
    {
      title: "Actions",
      key: "actions",
      width: 120,
      fixed: "right",
      render: (_: unknown, r: Conversation) => (
        <Button size="small" onClick={() => void openMessagesDrawer(r)}>
          messages
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
              key={`item-${itemIdFilter}`}
              placeholder="Item ID"
              allowClear
              inputMode="numeric"
              defaultValue={itemIdFilter}
              onSearch={applyItemIdFilter}
              style={{ width: 180 }}
            />
            <Input.Search
              key={`agent-${agentIdFilter}`}
              placeholder="Agent ID"
              allowClear
              inputMode="numeric"
              defaultValue={agentIdFilter}
              onSearch={applyAgentIdFilter}
              style={{ width: 180 }}
            />
          </div>
        }
      >
        {hasFilter ? (
          <Table
            dataSource={query.data?.data}
            columns={columns}
            rowKey="conv_id"
            loading={query.isLoading}
            scroll={{ x: 1100 }}
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
        ) : (
          <Empty description="Enter Item ID or Agent ID to search" />
        )}
      </List>

      <Drawer
        title={
          drawerConv
            ? `Conversation ${drawerConv.conv_id} — ${participantLabel(
                drawerConv.participant_a,
                drawerConv.participant_a_name,
              )} ↔ ${participantLabel(drawerConv.participant_b, drawerConv.participant_b_name)}`
            : "Messages"
        }
        placement="right"
        width={840}
        open={drawerOpen}
        onClose={closeDrawer}
        destroyOnClose
      >
        {messagesLoading ? (
          <div style={{ textAlign: "center", padding: 40 }}>
            <Spin />
          </div>
        ) : messages.length === 0 ? (
          <Empty description="No messages" />
        ) : (
          <Space direction="vertical" style={{ width: "100%" }} size={12}>
            {messages.map((m) => (
              <div
                key={m.msg_id}
                style={{
                  background: colorForSender(m.sender_id),
                  borderRadius: 8,
                  padding: 12,
                }}
              >
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    marginBottom: 6,
                    fontSize: 12,
                    color: "rgba(0,0,0,0.55)",
                  }}
                >
                  <span>
                    <strong>{m.sender_name || m.sender_id}</strong>
                    {m.sender_name ? ` (${m.sender_id})` : null}
                  </span>
                  <span>{formatTimestamp(m.created_at)}</span>
                </div>
                <Typography.Paragraph style={{ whiteSpace: "pre-wrap", marginBottom: 0 }}>
                  {m.content}
                </Typography.Paragraph>
              </div>
            ))}
          </Space>
        )}
      </Drawer>
    </>
  );
};
