# Desenvolvimento, CI e release

O contrato é o mesmo em todos os projetos: commit/push de código → CI completo → pedido explícito de release → reutilização da aprovação → build/empacotamento/publicação.

## Uso

```sh
npm run hooks:install
# Editar, fazer commit e push do código na branch main.
# Aguardar o CI de origem, ou deixar a release aguardar pela sua conclusão.
npm run release
# Ou indicar a versão diretamente:
npm run release -- X.Y.Z
```

O script de release exige uma árvore limpa. Prepara apenas os campos de versão permitidos; alterações de código ou de dependências externas obrigam a novo CI. O pedido de release não volta a executar os testes de origem. Se a aprovação estiver pendente, o publisher aguarda; se falhar ou não existir, para antes de construir/publicar. Para recuperar, corrige ou repete o CI do commit de origem e volta a executar o workflow de release que falhou.

A aprovação pertence ao commit de origem exato; não basta existir outro CI verde no repositório.

## Hooks e verificações locais

Os hooks ficam ativos com `npm run hooks:install` e são reinstalados no prepare npm. Os frontends com instalação separada também fazem a instalação quando os scripts da raiz estão presentes. Os builds de distribuição sem esses scripts saltam esta configuração.

- Antes do commit: formatação automática do conteúdo preparado para commit, com Vite+ e os formatadores nativos; as correções entram automaticamente no stage. NoteX mantém as verificações SCSS; voice usa Ruff; PC Configurator usa as regras migradas para Vite+ e preserva os testes impactados.
- Antes do push: `npm run prepush`, com as verificações próprias da stack. Um push composto apenas pelo pedido de release é reconhecido e deixa a aprovação para o GitHub, sem repetir os checks locais.
- `npm run check`: `node scripts/git-hooks.mjs check`.
- `NOX_SKIP_HOOKS=1` permite desativar os hooks num processo local; a aprovação obrigatória no GitHub continua ativa.

## CI e distribuição

[CI](.github/workflows/ci.yml) · [Release](.github/workflows/release.yml)

Constrói e publica a imagem multi-arquitetura `ghcr.io/mapherez/nox-gateway` e cria a GitHub Release.

Os builds de publicação continuam necessários para incorporar a nova versão, plataformas e assinatura. Smoke checks do artefacto final não equivalem a repetir a suite de testes de origem.

## Formatação nativa

Este projeto é Go e não tem frontend CSS/JS/TS. Mantém gofmt, vet e testes Go, com `.editorconfig` e `.gitattributes` para regras de ficheiros e LF. `npm run format` cobre Go, Python, scripts e documentação; o hook formata automaticamente e atualiza o stage. Vite+ cobre os ficheiros JS/JSON/YAML/Markdown de manutenção. Os scripts Node de release continuam disponíveis e as suas políticas são testadas no CI.

## Automatic formatting

`npm run format` formats all tracked source and text files. `npm run format:check`
checks the same files with the same configuration, without changing them.
The pre-commit hook formats staged contents and updates the index automatically,
including native languages. Unstaged edits stay unstaged; overlapping edits cause
an explicit error before any file or index change. Push reads the committed blobs (uncommitted fixes cannot mask an invalid commit).
Source CI verifies the same formatting; the push hook never rewrites an existing commit.

Vite+ formats JS/TS, JSX/TSX, CSS/SCSS/Less, HTML, JSON, YAML, TOML, Markdown and
Svelte where used. Go uses gofmt and go mod edit -fmt, Rust uses rustfmt, Python uses pinned Ruff via
uv, Prisma uses its CLI, and complementary formatters cover shell/Dockerfile,
PowerShell, SQL, XML/SVG and nginx configuration. Other UTF-8 text receives LF,
trailing-space cleanup and a final newline (Makefile tabs are preserved).
Install Node dependencies before committing, plus Go/Rust/uv for the native
languages present in the repository. Missing formatters fail with an error.

Binary assets, vendored code, licenses, machine-generated lockfiles and historical
SQL migrations are preserved. The exact immutable migration list is documented
in `format-policy.json` next to the formatter; new migration files are formatted.
Generated source must be formatted by its generator. Formatting options are shared
between Vite+ and the hooks through `format-options.json`. For standalone frontend
builds this JSON lives inside frontend/web and format-policy.json selects its path.
