/* icons.jsx — inline line icons (Lucide-ish), stroke=currentColor */
const ICON_PATHS = {
  dashboard: 'M3 3h7v7H3zM14 3h7v4h-7zM14 10h7v11h-7zM3 14h7v7H3z',
  devices: 'M4 5h12a1 1 0 0 1 1 1v7H3V6a1 1 0 0 1 1-1zM3 13h14M9 17h3M19 9h2a1 1 0 0 1 1 1v9a1 1 0 0 1-1 1h-2a1 1 0 0 1-1-1v-9a1 1 0 0 1 1-1z',
  alert: 'M12 9v4M12 17h.01M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z',
  check: 'M9 11l3 3L22 4M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11',
  tasks: 'M9 6h11M9 12h11M9 18h11M4 6h.01M4 12h.01M4 18h.01',
  policy: 'M12 2 4 5v6c0 5 3.5 8.5 8 10 4.5-1.5 8-5 8-10V5l-8-3zM9 12l2 2 4-4',
  script: 'M8 9l-3 3 3 3M16 9l3 3-3 3M13 7l-2 10',
  chat: 'M21 11.5a8.4 8.4 0 0 1-9 8.4 9 9 0 0 1-3.9-.9L3 21l1.9-5.1A8.4 8.4 0 0 1 12 3a8.4 8.4 0 0 1 9 8.5z',
  clients: 'M3 21V7l9-4 9 4v14M3 21h18M9 21v-6h6v6M7 10h.01M12 10h.01M17 10h.01',
  audit: 'M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8zM14 2v6h6M9 13h6M9 17h4',
  users: 'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8M22 21v-2a4 4 0 0 0-3-3.9M16 3.1a4 4 0 0 1 0 7.8',
  settings: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-2.9 1.2V22a2 2 0 0 1-4 0v-.1a1.7 1.7 0 0 0-2.9-1.2l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0-1.2-2.9H2a2 2 0 0 1 0-4h.1a1.7 1.7 0 0 0 1.2-2.9l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 2.9-1.2V2a2 2 0 0 1 4 0v.1a1.7 1.7 0 0 0 2.9 1.2l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0 1.2 2.9H22a2 2 0 0 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z',
  search: 'M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16zM21 21l-4.3-4.3',
  bell: 'M18 8a6 6 0 0 0-12 0c0 7-3 9-3 9h18s-3-2-3-9M13.7 21a2 2 0 0 1-3.4 0',
  plus: 'M12 5v14M5 12h14',
  close: 'M18 6 6 18M6 6l12 12',
  chevdown: 'M6 9l6 6 6-6',
  chevright: 'M9 6l6 6-6 6',
  chevleft: 'M15 6l-6 6 6 6',
  arrowright: 'M5 12h14M13 6l6 6-6 6',
  play: 'M6 4l14 8-14 8z',
  approve: 'M20 6 9 17l-5-5',
  reject: 'M18 6 6 18M6 6l12 12',
  clock: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 6v6l4 2',
  refresh: 'M3 12a9 9 0 0 1 15-6.7L21 8M21 3v5h-5M21 12a9 9 0 0 1-15 6.7L3 16M3 21v-5h5',
  copy: 'M9 9h10a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2h-8a2 2 0 0 1-2-2V11a2 2 0 0 1 2-2zM5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1',
  external: 'M15 3h6v6M10 14 21 3M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6',
  download: 'M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3',
  remote: 'M5 3h14a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2zM8 21h8M12 17v4',
  terminal: 'M4 4h16a1 1 0 0 1 1 1v14a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V5a1 1 0 0 1 1-1zM7 9l3 3-3 3M13 15h4',
  cpu: 'M6 6h12v12H6zM9 2v4M15 2v4M9 18v4M15 18v4M2 9h4M2 15h4M18 9h4M18 15h4',
  disk: 'M22 12a10 10 0 1 1-20 0 10 10 0 0 1 20 0zM12 12h.01M12 8a4 4 0 0 1 0 8',
  mem: 'M3 8h18v8H3zM7 12h.01M11 12h.01M15 12h.01M6 8V5M18 8V5',
  network: 'M5 12.5a7 7 0 0 1 14 0M8.5 15.5a3.5 3.5 0 0 1 7 0M2 9a11 11 0 0 1 20 0M12 19h.01',
  sparkle: 'M12 3l1.9 5.2L19 10l-5.1 1.8L12 17l-1.9-5.2L5 10l5.1-1.8zM19 3l.7 1.8L21 5.5l-1.3.7L19 8l-.7-1.8L17 5.5l1.3-.7zM6 16l.6 1.6L8 18l-1.4.7L6 20l-.6-1.6L4 18l1.4-.4z',
  shield: 'M12 2 4 5v6c0 5 3.5 8.5 8 10 4.5-1.5 8-5 8-10V5z',
  logout: 'M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9',
  filter: 'M22 3H2l8 9.5V19l4 2v-8.5z',
  dot: 'M12 12m-3 0a3 3 0 1 0 6 0 3 3 0 1 0-6 0',
  windows: 'M3 5.5 10 4.5v7H3zM10 4.4 21 3v8.5H10zM3 12.5h7v7l-7-1zM10 12.5h11V21l-11-1.5z',
  apple: 'M16 3c0 1.5-.9 3-2.2 3.7M12 8c-1.5 0-2.7-1-4.5-1C5 7 2.5 9 2.5 13.5 2.5 17 5 21 7.5 21c1.3 0 1.8-.8 3.5-.8s2.2.8 3.5.8c2.5 0 4.5-4 4.5-5.5-3-1-3-5.5 0-6.5C18 7.5 16 7 14.5 7 13.4 7 13 8 12 8z',
  linux: 'M12 2a4 4 0 0 0-4 4c0 2 .5 3 .5 5s-2 3-2 6c0 2 1.5 4 5.5 4s5.5-2 5.5-4c0-3-2-4-2-6s.5-3 .5-5a4 4 0 0 0-4-4zM10 8h.01M14 8h.01',
  grid: 'M3 3h7v7H3zM14 3h7v7h-7zM14 14h7v7h-7zM3 14h7v7H3z',
  list: 'M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01',
  history: 'M3 3v5h5M3.05 13A9 9 0 1 0 6 5.3L3 8M12 7v5l4 2',
  report: 'M3 3v18h18M7 16V10M12 16V6M17 16v-4',
  trash: 'M3 6h18M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6M10 11v6M14 11v6',
  edit: 'M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7M18.5 2.5a2.1 2.1 0 0 1 3 3L12 15l-4 1 1-4z',
  link: 'M10 13a5 5 0 0 0 7 0l3-3a5 5 0 0 0-7-7l-1.5 1.5M14 11a5 5 0 0 0-7 0l-3 3a5 5 0 0 0 7 7l1.5-1.5',
  flash: 'M13 2 3 14h7l-1 8 10-12h-7z',
  info: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16v-4M12 8h.01',
  send: 'M22 2 11 13M22 2l-7 20-4-9-9-4z',
  user: 'M20 21a8 8 0 1 0-16 0M12 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8z',
  wrench: 'M14.7 6.3a4 4 0 0 0 5 5l-9 9a2.8 2.8 0 0 1-4-4z',
  eye: 'M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z',
  lock: 'M5 11h14a2 2 0 0 1 2 2v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-7a2 2 0 0 1 2-2zM8 11V7a4 4 0 0 1 8 0v4',
  key: 'M14 7a4 4 0 1 1-5 5l-6 6v3h3l1-1h2v-2h2l1-1 2-2a4 4 0 0 0 0-8zM16 7h.01',
  building: 'M3 21h18M5 21V5a2 2 0 0 1 2-2h6a2 2 0 0 1 2 2v16M9 7h2M9 11h2M9 15h2M15 21v-9a1 1 0 0 1 1-1h2a1 1 0 0 1 1 1v9',
  pin: 'M12 21s-7-6.3-7-11a7 7 0 0 1 14 0c0 4.7-7 11-7 11zM12 11a2 2 0 1 0 0-4 2 2 0 0 0 0 4z',
  menu: 'M3 6h18M3 12h18M3 18h18',
  dots: 'M12 5m-1.6 0a1.6 1.6 0 1 0 3.2 0a1.6 1.6 0 1 0-3.2 0M12 12m-1.6 0a1.6 1.6 0 1 0 3.2 0a1.6 1.6 0 1 0-3.2 0M12 19m-1.6 0a1.6 1.6 0 1 0 3.2 0a1.6 1.6 0 1 0-3.2 0',
  sun: 'M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10zM12 1v2M12 21v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M1 12h2M21 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4',
  moon: 'M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z',
};

function Icon({ name, size = 16, className = '', strokeWidth = 1.7, fill = false, style }) {
  const d = ICON_PATHS[name] || ICON_PATHS.dot;
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill={fill ? 'currentColor' : 'none'}
      stroke={fill ? 'none' : 'currentColor'} strokeWidth={strokeWidth} strokeLinecap="round"
      strokeLinejoin="round" className={className} style={style} aria-hidden="true">
      {d.split('M').filter(Boolean).map((seg, i) => <path key={i} d={'M' + seg} />)}
    </svg>
  );
}

function OsIcon({ os, size = 15, className = '' }) {
  const o = (os || '').toLowerCase();
  let name = 'devices';
  if (o.includes('win')) name = 'windows';
  else if (o.includes('mac') || o.includes('darwin') || o.includes('osx')) name = 'apple';
  else if (o.includes('linux') || o.includes('ubuntu') || o.includes('debian') || o.includes('cent')) name = 'linux';
  return <Icon name={name} size={size} className={className} strokeWidth={1.6} />;
}

window.Icon = Icon;
window.OsIcon = OsIcon;
