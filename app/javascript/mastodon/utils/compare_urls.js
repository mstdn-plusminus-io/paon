export const compareUrls = (href1, href2) => {
  try {
    const url1 = new URL(href1);
    const url2 = new URL(href2);

    return url1.origin === url2.origin
      && url1.pathname === url2.pathname
      && url1.search === url2.search;
  } catch {
    return false;
  }
};
