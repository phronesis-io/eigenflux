import { Refine } from "@refinedev/core";
import { RefineThemes, ThemedLayout, useNotificationProvider } from "@refinedev/antd";
import { BrowserRouter, Routes, Route, Outlet, Navigate } from "react-router-dom";
import { App as AntdApp, ConfigProvider } from "antd";
import "@refinedev/antd/dist/reset.css";

import { consoleApiUrl } from "./config";
import { consoleDataProvider } from "./dataProvider";
import { AgentList } from "./pages/agents/list";
import { ImprRecordList } from "./pages/impr/list";
import { ItemList } from "./pages/items/list";
import { MilestoneRuleList } from "./pages/milestone-rules/list";
import { SystemNotificationList } from "./pages/system-notifications/list";

function App() {
  return (
    <BrowserRouter>
      <ConfigProvider theme={RefineThemes.Blue}>
        <AntdApp>
          <Refine
            dataProvider={consoleDataProvider(consoleApiUrl)}
            notificationProvider={useNotificationProvider}
            resources={[
              {
                name: "agents",
                list: "/agents",
              },
              {
                name: "items",
                list: "/items",
              },
              {
                name: "impr",
                list: "/impr",
                meta: {
                  label: "Impr Records",
                },
              },
              {
                name: "milestone-rules",
                list: "/milestone-rules",
                meta: {
                  label: "Milestone Rules",
                },
              },
              {
                name: "system-notifications",
                list: "/system-notifications",
                meta: {
                  label: "System Notifications",
                },
              },
            ]}
          >
            <Routes>
              <Route
                element={
                  <ThemedLayout>
                    <Outlet />
                  </ThemedLayout>
                }
              >
                <Route index element={<Navigate to="/agents" replace />} />
                <Route path="/agents" element={<AgentList />} />
                <Route path="/items" element={<ItemList />} />
                <Route path="/impr" element={<ImprRecordList />} />
                <Route path="/milestone-rules" element={<MilestoneRuleList />} />
                <Route path="/system-notifications" element={<SystemNotificationList />} />
              </Route>
            </Routes>
          </Refine>
        </AntdApp>
      </ConfigProvider>
    </BrowserRouter>
  );
}

export default App;
