export const shouldLoadMoreMedia = ({ scrollTop, scrollHeight, clientHeight, isLoading }) => (
  !isLoading && scrollHeight - scrollTop - clientHeight < 150
);
