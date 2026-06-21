// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import react from '@astrojs/react';
import starlightLlmsTxt from 'starlight-llms-txt';
import remarkGfm from 'remark-gfm';

export default defineConfig({
  site: 'https://miucr.vanducng.dev',
  // GFM (tables, strikethrough) for MDX — .mdx does not get it by default.
  // NOTE: markdown.remarkPlugins is deprecated in Astro 6; migrate when bumping major.
  markdown: { remarkPlugins: [remarkGfm] },
  integrations: [
    starlight({
      title: 'miu-cr',
      logo: { src: './src/assets/logo.svg' },
      // Apply Starlight's markdown pipeline (asides, heading links) to the custom-loader content/ dir.
      markdown: { processedDirs: ['./content'] },
      description: 'Owned local AI code-review CLI for humans and agents.',
      customCss: ['./src/styles/theme.css'],
      expressiveCode: {
        themes: ['catppuccin-mocha', 'catppuccin-latte'],
        styleOverrides: { borderRadius: '0.5rem' },
      },
      components: {
        ThemeSelect: './src/components/ThemeSelect.astro',
        SocialIcons: './src/components/SocialIcons.astro',
        Search: './src/components/Search.astro',
      },
      plugins: [
        starlightLlmsTxt({
          projectName: 'miu-cr',
          description: 'Owned local AI code-review CLI for humans and agents.',
        }),
      ],
      lastUpdated: true,
      sidebar: [
        { label: 'Introduction', link: '/' },
        { label: 'Getting Started', items: ['install', 'usage', 'rules'] },
        { label: 'Providers', items: ['providers', 'credentials'] },
        { label: 'Integration', items: ['mcp', 'github-pr', 'serve-and-action'] },
        { label: 'Internals', items: ['how-it-works', 'store-backends'] },
        {
          label: 'Related docs',
          items: [
            { label: 'miudb', link: 'https://miudb.vanducng.dev', attrs: { target: '_blank' } },
            { label: 'dotfiles', link: 'https://dotfiles.vanducng.dev', attrs: { target: '_blank' } },
            { label: 'skills', link: 'https://skills.vanducng.dev', attrs: { target: '_blank' } },
            { label: 'vd-cli', link: 'https://vd-cli.vanducng.dev', attrs: { target: '_blank' } },
          ],
        },
      ],
    }),
    react(),
  ],
});
