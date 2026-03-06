FROM python:3.12-alpine

RUN addgroup -S app && adduser -S app -G app

WORKDIR /app

COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt

COPY app.py ./
RUN mkdir -p /data && chown -R app:app /app /data

USER app

EXPOSE 8098
ENV PORT=8098

ENTRYPOINT ["python", "app.py"]
