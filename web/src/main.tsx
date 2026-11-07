import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import { api, ApiError, type User } from "./api.ts";
import { forgetKeys } from "./account.ts";
import { navigate, usePath } from "./router.tsx";
import { TopBar } from "./components/ui.tsx";
import { LoginPage, RecoverPage, SignupPage } from "./pages/Auth.tsx";
import { PCsPage } from "./pages/PCs.tsx";
import { AddPCPage } from "./pages/AddPC.tsx";
import { PeoplePage } from "./pages/People.tsx";

const PUBLIC = ["/login", "/signup", "/recover"];

function App() {
  const path = usePath();
  // undefined: still checking; null: logged out.
  const [user, setUser] = useState<User | null | undefined>(undefined);

  useEffect(() => {
    api.me().then(
      (me) => setUser(me.user),
      (e) => {
        if (e instanceof ApiError && e.status === 401) setUser(null);
        else setUser(null);
      },
    );
  }, []);

  const isPublic = PUBLIC.includes(path);

  useEffect(() => {
    if (user === null && !isPublic) navigate("/login" + (path !== "/" ? "?next=" + encodeURIComponent(path) : ""), true);
    if (user && (path === "/login" || path === "/recover")) navigate("/", true);
  }, [user, path, isPublic]);

  useEffect(() => {
    const titles: Record<string, string> = { "/login": "Log in", "/signup": "Sign up", "/recover": "Recover", "/add": "Add a PC", "/people": "People" };
    document.title = titles[path] ? `${titles[path]} · restash` : "restash";
  }, [path]);

  if (user === undefined) return null;
  if (path === "/login") return <LoginPage onUser={setUser} />;
  if (path === "/signup") return <SignupPage onUser={setUser} />;
  if (path === "/recover") return <RecoverPage onUser={setUser} />;
  if (!user) return null;

  const logout = async () => {
    await api.logout().catch(() => {});
    forgetKeys();
    setUser(null);
    navigate("/login", true);
  };

  let page;
  if (path === "/add") page = <AddPCPage />;
  else if (path === "/people" && user.admin) page = <PeoplePage />;
  else page = <PCsPage />;

  return (
    <>
      <TopBar user={user} onLogout={logout} />
      <main>{page}</main>
    </>
  );
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
