# Built from the repository root: pnpm --filter listener build first.
FROM node:24-slim
WORKDIR /app
COPY apps/listener/dist/main.mjs ./main.mjs
USER 10001:10001
ENV NODE_ENV=production
EXPOSE 8080
CMD ["node", "main.mjs"]
