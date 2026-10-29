import type { APIRoute, GetStaticPaths } from 'astro';
import { getCollection, type CollectionEntry } from 'astro:content';

// Every page is also served as raw Markdown at /<page>.md for agents.
export const getStaticPaths: GetStaticPaths = async () =>
  (await getCollection('docs')).map((entry) => ({ params: { slug: entry.id }, props: { entry } }));

export const GET: APIRoute = ({ props }) => {
  const { data, body } = props.entry as CollectionEntry<'docs'>;
  const head = `# ${data.title}\n\n${data.description ? `> ${data.description}\n\n` : ''}`;
  return new Response(head + (body ?? ''), { headers: { 'Content-Type': 'text/markdown; charset=utf-8' } });
};
