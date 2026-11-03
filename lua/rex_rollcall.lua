-- Written by `rex-rollcall install`. Rerun install after upgrading the binary.
local VERSION = "@VERSION@"

local M = { version = VERSION }

local cfg = {
  bin = "@BIN@",
  popup = "layer",
  bounds = { 0.05, 0.05, 0.9, 0.9 },
  deadline_ms = 1000,
  past_limit = 200,
  vim = false,
  sources = {},
}

local function call(method, args)
  local ok, res = pcall(rex.call, method, args)
  if ok then return res end
end

local function nonempty(t)
  if type(t) == "table" and next(t) ~= nil then return t end
end

local function collect()
  local app
  for _, c in ipairs((call("client.list", {}) or {}).clients or {}) do
    if not app and c.info and c.info.kind == "app" then app = c.client_id end
  end
  local blocks = {}
  for _, s in ipairs((call("session.list", {}) or {}).sessions or {}) do
    call("session.attach", { session_id = s.session_id })
    for _, b in ipairs((call("session.list_blocks", { session_id = s.session_id }) or {}).blocks or {}) do
      if b.placement == "placed" then
        local function method(name)
          return call("block.method", { session_id = s.session_id, block_id = b.block_id, method = name, args = {} })
        end
        local proc = method("process")
        if proc and proc.foreground then
          local status, title = method("program_status"), method("title")
          blocks[#blocks + 1] = {
            session_id = s.session_id,
            window_id = b.window_id or "",
            block_id = b.block_id,
            home = proc.home or "",
            title = title and title.title or "",
            fg = proc.foreground,
            records = nonempty(status and status.records),
          }
        end
      end
    end
  end
  return nonempty(blocks), app
end

local function host_op(_, args)
  args = args or {}
  args.version = VERSION
  args.sources = nonempty(cfg.sources)
  args.deadline_ms = args.deadline_ms or cfg.deadline_ms
  if args.op == "rows" then
    args.blocks, args.app_client = collect()
  end
  if args.op == "rows" or args.op == "search" then
    args.server_id = (call("server.status", {}) or {}).host_id
  end
  local r, err = rex.exec({ cfg.bin, "_op" }, {
    stdin = rex.json.encode(args),
    timeout = math.min(25, args.deadline_ms / 1000 + 2),
  })
  if not r then error("rex-rollcall: " .. tostring(err), 0) end
  if not r.ok then error("rex-rollcall _op: " .. (r.stderr ~= "" and r.stderr or ("exit " .. tostring(r.code))), 0) end
  return r.stdout
end

local function open(ctx, popup, demo)
  local from_user = ctx.origin == "key" or ctx.origin == "palette" or ctx.origin == nil
  local layout = rex.layout.block{
    flavor = "com.superlogical.terminal.shell",
    label = "Rollcall",
    options = {
      command = {
        "/usr/bin/env",
        "REX_ROLLCALL_ANCHOR=" .. rex.json.encode{
          session = ctx.session_id or "",
          block = ctx.block_id or "",
          client = from_user and ctx.client_id or "",
        },
        "REX_ROLLCALL_SETTINGS=" .. rex.json.encode{
          deadline_ms = cfg.deadline_ms,
          past_limit = cfg.past_limit,
          unlock = cfg.unlock ~= nil,
          unlock_host = cfg.unlock and cfg.unlock.via or "",
          vim = cfg.vim,
          demo = demo == true,
        },
        cfg.bin,
      },
      exit = { on_completion = true, quick_exit_threshold_ms = 0 },
    },
  }
  local split = function()
    return rex.session.new_split{
      anchor_block_id = ctx.block_id, direction = "vertical", side = "after",
      ratio = 0.5, focus = true, layout = layout,
    }
  end
  local _, err
  if (popup or cfg.popup) == "split" then
    _, err = split()
  else
    _, err = rex.session.new_layer{ bounds = cfg.bounds, focus = true, layout = layout }
    if err ~= nil and ctx.block_id then _, err = split() end
  end
  if err ~= nil then error(err, 0) end
end

local function expand(path)
  if path:sub(1, 2) == "~/" then return os.getenv("HOME") .. path:sub(2) end
  return path
end

local function unlock_over_ssh()
  local u = cfg.unlock
  local argv = { "ssh", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes" }
  if u.identity_agent then
    argv[#argv + 1] = "-o"
    argv[#argv + 1] = 'IdentityAgent="' .. expand(u.identity_agent) .. '"'
  end
  for _, a in ipairs({ "-i", expand(u.identity_file), "-l", u.user or "root", u.host }) do
    argv[#argv + 1] = a
  end
  local r, err = rex.exec(argv, { timeout = 25 })
  if not r then error("rollcall_unlock: " .. tostring(err), 0) end
  if not r.ok then error("rollcall_unlock: " .. (r.stderr ~= "" and r.stderr or ("exit " .. tostring(r.code))), 0) end
  return r.stdout
end

local function check(cond, msg)
  if not cond then error("rex_rollcall.setup: " .. msg, 3) end
end

local function strings(t)
  if type(t) ~= "table" or #t == 0 then return false end
  for _, v in ipairs(t) do
    if type(v) ~= "string" then return false end
  end
  return true
end

function M.setup(opts)
  opts = opts or {}
  check(type(opts) == "table", "expects a table")
  local keys = opts.key or {}
  if type(keys) == "string" then keys = { keys } end
  check(type(keys) == "table", "key must be a string or a list of strings")
  check(opts.popup == nil or opts.popup == "layer" or opts.popup == "split", 'popup must be "layer" or "split"')
  check(opts.bin == nil or type(opts.bin) == "string", "bin must be a string")
  check(opts.vim == nil or type(opts.vim) == "boolean", "vim must be true or false")
  for _, n in ipairs({ "deadline_ms", "past_limit" }) do
    check(opts[n] == nil or (type(opts[n]) == "number" and opts[n] > 0), n .. " must be a positive number")
  end
  local sources = {}
  for i, s in ipairs(opts.sources or {}) do
    check(type(s) == "table" and type(s.name) == "string" and s.name ~= "", "sources[" .. i .. "].name must be a string")
    check(strings(s.wrap), "sources[" .. i .. "].wrap must be a non-empty list of strings")
    check(type(s.bin) == "string" and s.bin:sub(1, 1) == "/", "sources[" .. i .. "].bin must be an absolute path")
    check(s.user == nil or type(s.user) == "string", "sources[" .. i .. "].user must be a string")
    check(s.previews == nil or s.previews == "never" or s.previews == "unlock",
      "sources[" .. i .. '].previews must be "never" or "unlock"')
    sources[#sources + 1] = { name = s.name, wrap = s.wrap, bin = s.bin, user = s.user, previews = s.previews or "never" }
  end
  local u = opts.unlock
  if u ~= nil then
    check(type(u) == "table", "unlock must be a table")
    check((u.via ~= nil) ~= (u.host ~= nil), "unlock needs either via (a Rex host label) or host (an ssh host)")
    check(u.via == nil or type(u.via) == "string", "unlock.via must be a string")
    check(u.host == nil or type(u.identity_file) == "string", "unlock.identity_file must be a path to the key")
    for _, n in ipairs({ "host", "user", "identity_agent" }) do
      check(u[n] == nil or type(u[n]) == "string", "unlock." .. n .. " must be a string")
    end
  end
  cfg.unlock = u

  for _, n in ipairs({ "bin", "popup", "bounds", "deadline_ms", "past_limit", "vim" }) do
    if opts[n] ~= nil then cfg[n] = opts[n] end
  end
  cfg.sources = sources

  rex.action{
    name = "rollcall",
    title = "Agent Sessions",
    description = "Jump to a live coding agent or resume a past session",
    keywords = "claude codex opencode pi hermes wake resume rex-rollcall",
    args = { popup = "string?", demo = "boolean?" },
    run = function(ctx, args) open(ctx, args and args.popup, args and args.demo) end,
  }
  rex.action{
    name = "rollcall_op",
    title = "rex-rollcall host op",
    description = "Internal: answers the rex-rollcall picker for this host",
    category = "rex-rollcall",
    run = host_op,
  }
  if u and u.host then
    rex.action{
      name = "rollcall_unlock",
      title = "Rollcall: Unlock Previews",
      description = "Unlock root transcript previews for a few minutes (Touch ID through ssh)",
      category = "rex-rollcall",
      run = unlock_over_ssh,
    }
  end
  for _, k in ipairs(keys) do
    check(type(k) == "string", "key must be a string or a list of strings")
    rex.bind(k, function(ctx)
      open(ctx)
      return true
    end)
  end
  return M
end

return M
