#!/usr/bin/env bash
#
# install.sh — 安装 uvdoc CLI，安装 skills，并注册 profile。
#
# 本脚本需与 uvdoc 可执行文件、skills/ 目录位于同一目录下（即 cli/）。
#
# 用法:
#   ./install.sh <skills-dir> <server> [install-dir]
#
# 参数:
#   <skills-dir>  (必填) skills 目录的目标位置，例如 ~/.claude/skills
#   <server>      (必填) WeKnora 服务端地址，例如 https://kb.example.com
#   [install-dir] (选填) uvdoc 的安装目录，默认 /usr/local/bin
#
# 示例:
#   ./install.sh ~/.claude/skills https://kb.example.com
#   ./install.sh ~/.claude/skills https://kb.example.com ~/.local/bin

set -uo pipefail

# ---------------------------------------------------------------------------
# 输出辅助
# ---------------------------------------------------------------------------
if [ -t 1 ]; then
  GREEN=$'\033[0;32m'; RED=$'\033[0;31m'; YELLOW=$'\033[0;33m'; CYAN=$'\033[0;36m'; NC=$'\033[0m'
else
  GREEN=""; RED=""; YELLOW=""; CYAN=""; NC=""
fi

info() { printf '%s\n' "${CYAN}==>${NC} $*"; }
ok()   { printf '%s\n' "${GREEN}[成功]${NC} $*"; }
warn() { printf '%s\n' "${YELLOW}[警告]${NC} $*"; }
die()  { printf '%s\n' "${RED}[失败]${NC} $*" >&2; exit 1; }

usage() {
  cat <<EOF
用法: $(basename "$0") <skills-dir> <server> [install-dir]

  <skills-dir>  (必填) skills 目录的目标位置，例如 ~/.claude/skills
  <server>      (必填) WeKnora 服务端地址，例如 https://kb.example.com
  [install-dir] (选填) uvdoc 的安装目录，默认 /usr/local/bin

示例:
  $(basename "$0") ~/.claude/skills https://kb.example.com
  $(basename "$0") ~/.claude/skills https://kb.example.com ~/.local/bin
EOF
}

# ---------------------------------------------------------------------------
# 参数校验
# ---------------------------------------------------------------------------
if [ "$#" -lt 2 ]; then
  usage
  die "缺少必填参数（需要 2 个，实际收到 $# 个）"
fi

SKILLS_DEST=$1
SERVER=$2
BIN_DIR=${3:-/usr/local/bin}

[ -n "$SKILLS_DEST" ] || { usage; die "<skills-dir> 不能为空"; }
[ -n "$SERVER" ]      || { usage; die "<server> 不能为空"; }

# ---------------------------------------------------------------------------
# 路径解析（脚本所在目录 = uvdoc / skills 所在目录）
# ---------------------------------------------------------------------------
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
UVOC_SRC="$SCRIPT_DIR/uvdoc"
SKILLS_SRC="$SCRIPT_DIR/skills"
UVOC_BIN="$BIN_DIR/uvdoc"

[ -f "$UVOC_SRC" ]  || die "未找到 uvdoc 可执行文件: $UVOC_SRC"
[ -d "$SKILLS_SRC" ] || die "未找到 skills 目录: $SKILLS_SRC"

echo "=========================================="
echo " uvdoc 安装脚本"
echo "   uvdoc 源文件 : $UVOC_SRC"
echo "   安装目录     : $BIN_DIR"
echo "   skills 目标  : $SKILLS_DEST"
echo "   server       : $SERVER"
echo "=========================================="

# ---------------------------------------------------------------------------
# 步骤 1/3: 赋予执行权限并安装 uvdoc
# ---------------------------------------------------------------------------
info "步骤 1/3: 为 uvdoc 添加执行权限并安装到 $BIN_DIR"
if chmod +x "$UVOC_SRC"; then
  ok "已添加执行权限: $UVOC_SRC"
else
  die "添加执行权限失败: $UVOC_SRC"
fi

SUDO=""
if [ -d "$BIN_DIR" ] && [ ! -w "$BIN_DIR" ]; then
  SUDO="sudo"
  warn "$BIN_DIR 不可写，将使用 sudo 执行安装"
elif [ ! -d "$BIN_DIR" ]; then
  SUDO="sudo"
  warn "$BIN_DIR 不存在，将使用 sudo 创建"
fi

# shellcheck disable=SC2086
if $SUDO mkdir -p "$BIN_DIR" && $SUDO mv "$UVOC_SRC" "$UVOC_BIN"; then
  ok "已安装: $UVOC_BIN"
else
  die "安装 uvdoc 失败: $UVOC_SRC -> $UVOC_BIN"
fi

# ---------------------------------------------------------------------------
# 步骤 2/3: 移动 skills 到指定目录
# ---------------------------------------------------------------------------
info "步骤 2/3: 移动 skills 到 $SKILLS_DEST"
if ! mkdir -p "$SKILLS_DEST"; then
  die "创建目标目录失败: $SKILLS_DEST"
fi
SKILLS_DEST=$(cd "$SKILLS_DEST" && pwd)

moved=0
for skill in "$SKILLS_SRC"/*/; do
  [ -d "$skill" ] || continue
  name=$(basename "$skill")
  target="$SKILLS_DEST/$name"

  if [ -e "$target" ]; then
    warn "目标已存在，将覆盖: $target"
    rm -rf "$target" || die "覆盖已存在的目录失败: $target"
  fi

  if mv "$skill" "$target"; then
    ok "已移动 skill: $name -> $target"
    moved=$((moved + 1))
  else
    die "移动 skill 失败: $name"
  fi
done

[ "$moved" -gt 0 ] || die "$SKILLS_SRC 下没有可移动的 skill"
ok "共移动 $moved 个 skill 到 $SKILLS_DEST"

# ---------------------------------------------------------------------------
# 步骤 3/3: 注册 profile
# ---------------------------------------------------------------------------
info "步骤 3/3: 注册 profile 'prod' (host=$SERVER)"
if "$UVOC_BIN" profile add prod --host "$SERVER" --use; then
  ok "已注册并激活 profile: prod (host=$SERVER)"
else
  die "执行 'uvdoc profile add prod --host $SERVER --use' 失败"
fi

# ---------------------------------------------------------------------------
# 收尾检查
# ---------------------------------------------------------------------------
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) warn "$BIN_DIR 不在当前 PATH 中，请将其加入 PATH 后再使用 uvdoc" ;;
esac

echo
ok "全部完成！uvdoc 已安装，skills 已移动，profile 'prod' 已就绪。"
echo "    下一步可执行: uvdoc auth login"
